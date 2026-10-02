//go:build windows

package nestedjob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	goruntime "runtime"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procThreadAttributeJobList is PROC_THREAD_ATTRIBUTE_JOB_LIST (Windows 10).
const procThreadAttributeJobList = 0x0002000d

// envInvocationID is the variable WinUnit injects into every unit process.
const envInvocationID = "WINUNIT_INVOCATION_ID"

var (
	kernel32                    = windows.NewLazySystemDLL("kernel32.dll")
	procIsProcessInJob          = kernel32.NewProc("IsProcessInJob")
	procGetHandleInformation    = kernel32.NewProc("GetHandleInformation")
	procK32GetProcessMemoryInfo = kernel32.NewProc("K32GetProcessMemoryInfo")
	procSuspendThread           = kernel32.NewProc("SuspendThread")
)

// IsProcessInJob reports membership in one specific job. A zero job asks
// whether the process is in any job, which never identifies a layer.
func IsProcessInJob(process, job windows.Handle) (bool, error) {
	var in int32
	r1, _, e1 := procIsProcessInJob.Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&in)))
	if r1 == 0 {
		return false, callError(e1)
	}
	return in != 0, nil
}

func handleInheritable(h windows.Handle) (bool, error) {
	var flags uint32
	r1, _, e1 := procGetHandleInformation.Call(uintptr(h), uintptr(unsafe.Pointer(&flags)))
	if r1 == 0 {
		return false, callError(e1)
	}
	return flags&windows.HANDLE_FLAG_INHERIT != 0, nil
}

// processMemoryCounters is PROCESS_MEMORY_COUNTERS_EX.
type processMemoryCounters struct {
	Cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

// PrivateBytes is the process's committed private memory (PrivateUsage).
func PrivateBytes(process windows.Handle) (uint64, error) {
	var c processMemoryCounters
	c.Cb = uint32(unsafe.Sizeof(c))
	r1, _, e1 := procK32GetProcessMemoryInfo.Call(uintptr(process), uintptr(unsafe.Pointer(&c)), uintptr(c.Cb))
	if r1 == 0 {
		return 0, callError(e1)
	}
	return uint64(c.PrivateUsage), nil
}

func suspendThread(h windows.Handle) (uint32, error) {
	r1, _, e1 := procSuspendThread.Call(uintptr(h))
	if r1 == 0xffffffff {
		return 0, callError(e1)
	}
	return uint32(r1), nil
}

func callError(e error) error {
	if e != nil && !errors.Is(e, windows.ERROR_SUCCESS) {
		return e
	}
	return errors.New("native call failed without an error code")
}

func filetime64(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

// CreationTime is the process creation FILETIME in 100 ns units.
func CreationTime(process windows.Handle) (uint64, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	return filetime64(created), nil
}

// CPUTime is the process's accumulated kernel plus user time in 100 ns units.
func CPUTime(process windows.Handle) (uint64, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	return filetime64(kernel) + filetime64(user), nil
}

func win32Code(err error) uint32 {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return uint32(errno)
	}
	return 0
}

func failure(op string, err error) *Failure {
	if err == nil {
		return nil
	}
	return &Failure{Op: op, Win32: win32Code(err), Message: err.Error()}
}

func osVersion() string {
	v := windows.RtlGetVersion()
	return fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
}

func imageHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// selfIdentity describes the calling process from its own token and image.
func selfIdentity(role string, generation int) (Identity, error) {
	created, err := CreationTime(windows.CurrentProcess())
	if err != nil {
		return Identity{}, fmt.Errorf("own creation time: %w", err)
	}
	image, err := os.Executable()
	if err != nil {
		return Identity{}, err
	}
	sum, err := imageHash(image)
	if err != nil {
		return Identity{}, fmt.Errorf("hash own image: %w", err)
	}
	tok := windows.GetCurrentProcessToken()
	user, err := tok.GetTokenUser()
	if err != nil {
		return Identity{}, fmt.Errorf("own token user: %w", err)
	}
	pid := windows.GetCurrentProcessId()
	var session uint32
	if err := windows.ProcessIdToSessionId(pid, &session); err != nil {
		return Identity{}, fmt.Errorf("own session: %w", err)
	}
	return Identity{
		Role: role, PID: pid, Created: created, Image: image, ImageSHA256: sum,
		SID: user.User.Sid.String(), Session: session, Elevated: tok.IsElevated(),
		Invocation: os.Getenv(envInvocationID), Generation: generation,
	}, nil
}

// probeHandle queries a numeric handle value as a job. Finding a job there
// means a job handle was inherited; any error is the expected result.
func probeHandle(value uint64) HandleProbe {
	p := HandleProbe{Value: value}
	var info windows.JOBOBJECT_BASIC_LIMIT_INFORMATION
	err := windows.QueryInformationJobObject(windows.Handle(value), windows.JobObjectBasicLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	if err == nil {
		p.IsJob = true
		p.Message = "limit flags " + strconv.FormatUint(uint64(info.LimitFlags), 16)
		return p
	}
	p.Win32, p.Message = win32Code(err), err.Error()
	return p
}

// jobLimitFlags returns the job's basic limit flags.
func jobLimitFlags(job windows.Handle) (uint32, error) {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return 0, err
	}
	return info.BasicLimitInformation.LimitFlags, nil
}

// jobCPURateControl is JOBOBJECT_CPU_RATE_CONTROL_INFORMATION.
type jobCPURateControl struct {
	ControlFlags uint32
	Value        uint32
}

func jobCPUFlags(job windows.Handle) (uint32, error) {
	var cpu jobCPURateControl
	if err := windows.QueryInformationJobObject(job, windows.JobObjectCpuRateControlInformation,
		uintptr(unsafe.Pointer(&cpu)), uint32(unsafe.Sizeof(cpu)), nil); err != nil {
		return 0, err
	}
	return cpu.ControlFlags, nil
}

// startProcess creates one fixture process from this image. It never
// inherits handles unless inherit lists them explicitly, and the returned
// handles are owned by the caller on success.
func startProcess(argv []string, flags uint32, jobList []windows.Handle, inherit []windows.Handle) (windows.ProcessInformation, error) {
	var pi windows.ProcessInformation
	app, err := windows.UTF16PtrFromString(argv[0])
	if err != nil {
		return pi, err
	}
	cmdLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return pi, err
	}
	var si windows.StartupInfoEx
	si.Cb = uint32(unsafe.Sizeof(si.StartupInfo))
	attrs := 0
	if len(jobList) > 0 {
		attrs++
	}
	if len(inherit) > 0 {
		attrs++
	}
	var list *windows.ProcThreadAttributeListContainer
	if attrs > 0 {
		list, err = windows.NewProcThreadAttributeList(uint32(attrs))
		if err != nil {
			return pi, fmt.Errorf("attribute list: %w", err)
		}
		defer list.Delete()
		if len(jobList) > 0 {
			// The attribute stores a pointer; the container keeps the slice
			// alive until Delete, after CreateProcess has returned.
			if err := list.Update(procThreadAttributeJobList, unsafe.Pointer(&jobList[0]), uintptr(len(jobList))*unsafe.Sizeof(jobList[0])); err != nil {
				return pi, &unavailableError{err: err}
			}
		}
		if len(inherit) > 0 {
			if err := list.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&inherit[0]), uintptr(len(inherit))*unsafe.Sizeof(inherit[0])); err != nil {
				return pi, fmt.Errorf("handle list: %w", err)
			}
		}
		si.Cb = uint32(unsafe.Sizeof(si))
		si.ProcThreadAttributeList = list.List()
		flags |= windows.EXTENDED_STARTUPINFO_PRESENT
	}
	// Detached: no console, so no console host process joins the jobs.
	err = windows.CreateProcess(app, cmdLine, nil, nil, len(inherit) > 0, flags|windows.DETACHED_PROCESS, nil, nil, &si.StartupInfo, &pi)
	// Keep the attribute storage and handle arrays alive through creation.
	goruntime.KeepAlive(jobList)
	goruntime.KeepAlive(inherit)
	goruntime.KeepAlive(list)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	return pi, nil
}

// unavailableError marks a JOB_LIST attribute the OS rejected: an explicit
// unqualified lane, never a reason to fall back to assignment.
type unavailableError struct{ err error }

func (e *unavailableError) Error() string { return "PROC_THREAD_ATTRIBUTE_JOB_LIST: " + e.err.Error() }
func (e *unavailableError) Unwrap() error { return e.err }

// childIdentity records a created process from its creator's handle.
func childIdentity(role string, pi windows.ProcessInformation, parent Identity) (Identity, error) {
	created, err := CreationTime(pi.Process)
	if err != nil {
		return Identity{}, err
	}
	return Identity{
		Role: role, PID: pi.ProcessId, Created: created,
		ParentPID: parent.PID, ParentCreated: parent.Created, Generation: parent.Generation,
	}, nil
}

// openIdentity opens an exact process by PID and creation time.
func openIdentity(id Identity, access uint32) (windows.Handle, error) {
	h, err := windows.OpenProcess(access|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, id.PID)
	if err != nil {
		return 0, fmt.Errorf("open %s pid %d: %w", id.Role, id.PID, err)
	}
	created, err := CreationTime(h)
	if err != nil {
		_ = windows.CloseHandle(h)
		return 0, fmt.Errorf("creation time of %s pid %d: %w", id.Role, id.PID, err)
	}
	if created != id.Created {
		_ = windows.CloseHandle(h)
		return 0, fmt.Errorf("%s pid %d was reused", id.Role, id.PID)
	}
	return h, nil
}

// CheckGone classifies an identity that nobody may be holding open. Only
// gone, exited and reused prove absence; an access error proves nothing.
func CheckGone(id Identity) PreviousCheck {
	c := PreviousCheck{Role: id.Role, PID: id.PID, Created: id.Created}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, id.PID)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		c.State = PreviousGone
		return c
	}
	if err != nil {
		c.State, c.Error = PreviousUnknown, err.Error()
		return c
	}
	defer windows.CloseHandle(h)
	created, err := CreationTime(h)
	if err != nil {
		c.State, c.Error = PreviousUnknown, err.Error()
		return c
	}
	if created != id.Created {
		c.State = PreviousReused
		return c
	}
	done, err := signaled(h)
	switch {
	case err != nil:
		c.State, c.Error = PreviousUnknown, err.Error()
	case done:
		c.State = PreviousExited
	default:
		c.State = PreviousRunning
	}
	return c
}

// signaled reports whether a held process handle has exited.
func signaled(h windows.Handle) (bool, error) {
	state, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false, err
	}
	switch state {
	case windows.WAIT_OBJECT_0:
		return true, nil
	case uint32(windows.WAIT_TIMEOUT):
		return false, nil
	default:
		return false, fmt.Errorf("wait state %d", state)
	}
}
