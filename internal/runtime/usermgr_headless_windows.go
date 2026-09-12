//go:build windows

package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func validateHeadlessLaunch(tok windows.Token, spec UserManagerSpec) error {
	if spec.Env != nil || len(spec.cmdArgv) != 0 {
		return errors.New("headless profile launch requires the target profile environment and manager entry point")
	}
	if !runningAsLocalSystem() || spec.Daemon == nil || !filepath.IsAbs(spec.Exe) {
		return errors.New("headless profile launch requires SYSTEM, an absolute executable and a broker job")
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		return err
	}
	if session != 0 {
		return errors.New("headless broker must run in session zero")
	}
	identity, err := tok.GetTokenUser()
	if err != nil {
		return err
	}
	if identity.User.Sid.String() != spec.SID {
		return errors.New("headless token SID mismatch")
	}
	_, domain, _, err := identity.User.Sid.LookupAccount("")
	if err != nil {
		return err
	}
	host, err := os.Hostname()
	if err != nil {
		return err
	}
	if !strings.EqualFold(domain, host) {
		return errors.New("managed profile loading currently requires a local machine account")
	}
	job := spec.Daemon
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.handle == 0 || !job.broker {
		return errors.New("headless profile launch requires an open broker job")
	}
	member, err := isProcessInJob(windows.CurrentProcess(), job.handle)
	if err != nil {
		return err
	}
	if !member {
		return errors.New("headless profile launch requires broker self-assignment")
	}
	return nil
}

func startHeadlessUserManager(tok windows.Token, spec UserManagerSpec) (UserManagerProc, error) {
	if err := validateHeadlessLaunch(tok, spec); err != nil {
		return nil, err
	}
	lease, desktop, err := startUserDesktop(spec)
	if lease == nil {
		return nil, err
	}
	if err != nil {
		return finishProfileLaunch(spec.SID, nil, lease, err)
	}
	job, err := OpenDaemonJob()
	if err != nil {
		return finishProfileLaunch(spec.SID, nil, lease, err)
	}
	lease.pending = job
	p, err := createHeadlessUserManager(tok, spec, job, desktop)
	var proc UserManagerProc
	if p != nil {
		lease.pending = nil
		proc = p
	}
	return finishProfileLaunch(spec.SID, proc, lease, err)
}

func createHeadlessUserManager(tok windows.Token, spec UserManagerSpec, job *DaemonJob, desktop string) (*userMgrProc, error) {
	app, err := windows.UTF16PtrFromString(spec.Exe)
	if err != nil {
		return nil, err
	}
	argv := append([]string{spec.Exe}, UserManagerArgs(spec.SID, spec.ExtraArgs)...)
	command := windows.ComposeCommandLine(argv)
	cmd, err := windows.UTF16FromString(command)
	if err != nil {
		return nil, err
	}
	// CreateProcessWithTokenW has a smaller documented command-line limit.
	if len(cmd) > 1024 {
		return nil, errors.New("headless command line exceeds 1024 UTF-16 characters")
	}
	desktopp, err := windows.UTF16PtrFromString(desktop)
	if err != nil {
		return nil, err
	}
	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Desktop: desktopp}
	var pi windows.ProcessInformation
	create := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateProcessWithTokenW")
	if err := create.Find(); err != nil {
		return nil, err
	}
	// LOGON_WITH_PROFILE makes Windows own the profile reference through the
	// child's lifetime, including abrupt broker death. A nil environment comes
	// from the target profile; no broker handles or environment are inherited.
	// The user entry point resolves known folders and normalizes user variables.
	flags := windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW
	ok, _, callErr := create.Call(uintptr(tok), 1, uintptr(unsafe.Pointer(app)), uintptr(unsafe.Pointer(&cmd[0])), uintptr(flags), 0, 0, uintptr(unsafe.Pointer(&si)), uintptr(unsafe.Pointer(&pi)))
	goruntime.KeepAlive(app)
	goruntime.KeepAlive(cmd)
	goruntime.KeepAlive(si)
	if ok == 0 {
		return nil, fmt.Errorf("CreateProcessWithTokenW: %w", callErr)
	}
	p := &userMgrProc{sid: spec.SID, pid: int(pi.ProcessId), process: pi.Process, thread: pi.Thread, job: job, unassigned: true}
	// This API cannot use the extended job-list attribute. Session-zero child
	// creation inherits the verified broker root; verify it before resuming and
	// attach the dedicated job while the primary thread remains suspended.
	spec.Daemon.mu.Lock()
	member, err := isProcessInJob(pi.Process, spec.Daemon.handle)
	spec.Daemon.mu.Unlock()
	if err != nil {
		return p, err
	}
	if !member {
		return p, errors.New("headless child did not inherit broker job")
	}
	if err := job.Assign(pi.Process); err != nil {
		return p, err
	}
	p.unassigned = false
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		return p, err
	}
	if err := windows.CloseHandle(pi.Thread); err != nil {
		return p, err
	}
	p.thread = 0
	return p, nil
}
