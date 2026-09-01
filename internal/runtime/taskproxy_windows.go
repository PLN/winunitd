//go:build windows

package runtime

import (
	"context"
	"fmt"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	schedSTaskAlreadyRunning = 0x0004131D
	schedETaskNotRunning     = 0x8004130B
	rpcEChangedMode          = 0x80010106

	taskCreateOrUpdate        = 6
	taskLogonInteractiveToken = 3
	sFalse                    = 1
	vtEmpty                   = 0
	vtI4                      = 3
)

var (
	clsidTaskScheduler = windows.GUID{
		Data1: 0x0F87369F,
		Data2: 0xA4E5,
		Data3: 0x4CFC,
		Data4: [8]byte{0xBD, 0x3E, 0x73, 0xE6, 0x15, 0x45, 0x72, 0xDD},
	}
	iidITaskService = windows.GUID{
		Data1: 0x2FABA4C7,
		Data2: 0x4DA9,
		Data3: 0x4013,
		Data4: [8]byte{0x96, 0x97, 0x20, 0xCC, 0x3F, 0xD4, 0x0F, 0x85},
	}

	modOle32             = windows.NewLazySystemDLL("ole32.dll")
	modOleAut32          = windows.NewLazySystemDLL("oleaut32.dll")
	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")
	procSysAllocString   = modOleAut32.NewProc("SysAllocString")
	procSysFreeString    = modOleAut32.NewProc("SysFreeString")
)

type winTasks struct{}

// DefaultTaskScheduler connects to the local Task Scheduler for
// Type=scheduled-task orchestration. It never creates, changes, deletes,
// enables, or disables task definitions.
func DefaultTaskScheduler() TaskScheduler {
	return winTasks{}
}

func (winTasks) Start(ctx context.Context, name string, timeout time.Duration) (TaskStatus, error) {
	return withRegisteredTask(name, func(task *iRegisteredTask) (TaskStatus, error) {
		st, err := queryRegisteredTask(task)
		if err != nil {
			return TaskStatus{}, err
		}
		if st.ActiveState() == "active" {
			return st, nil
		}
		if err := task.Run(); err != nil && !isTaskAlreadyRunning(err) {
			st, qerr := queryRegisteredTask(task)
			if qerr == nil && st.ActiveState() == "active" {
				return st, nil
			}
			return st, fmt.Errorf("Run %s: %w", name, err)
		}
		return waitTask(ctx, task, name, true, timeout)
	})
}

func (winTasks) Stop(ctx context.Context, name string, timeout time.Duration) (TaskStatus, error) {
	return withRegisteredTask(name, func(task *iRegisteredTask) (TaskStatus, error) {
		st, err := queryRegisteredTask(task)
		if err != nil {
			return TaskStatus{}, err
		}
		if st.ActiveState() != "active" && st.State != TaskQueued {
			return st, nil
		}
		if err := task.Stop(); err != nil && !isTaskAlreadyStopped(err) {
			st, qerr := queryRegisteredTask(task)
			if qerr == nil && st.ActiveState() != "active" && st.State != TaskQueued {
				return st, nil
			}
			return st, fmt.Errorf("Stop %s: %w", name, err)
		}
		return waitTask(ctx, task, name, false, timeout)
	})
}

func (winTasks) Query(name string) (TaskStatus, error) {
	return withRegisteredTask(name, queryRegisteredTask)
}

func waitTask(ctx context.Context, task *iRegisteredTask, name string, starting bool, timeout time.Duration) (TaskStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout = taskWaitTimeout(timeout)
	deadline := time.Now().Add(timeout)
	want := "active"
	if !starting {
		want = "inactive"
	}
	for {
		st, err := queryRegisteredTask(task)
		if err != nil {
			return TaskStatus{}, fmt.Errorf("query task %s: %w", name, err)
		}
		got := st.ActiveState()
		if starting && got == "active" {
			return st, nil
		}
		if !starting && got != "active" && st.State != TaskQueued {
			return st, nil
		}
		if starting && got == "failed" {
			return st, fmt.Errorf("task %s entered unknown state while starting", name)
		}
		if err := ctx.Err(); err != nil {
			return st, fmt.Errorf("timeout waiting for %s to become %s: %w", name, want, err)
		}
		if time.Now().After(deadline) {
			return st, fmt.Errorf("timeout waiting for %s to become %s", name, want)
		}
		sleep := defaultTaskPoll
		if rem := time.Until(deadline); rem > 0 && rem < sleep {
			sleep = rem
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return st, fmt.Errorf("timeout waiting for %s to become %s: %w", name, want, ctx.Err())
		case <-timer.C:
		}
	}
}

func queryRegisteredTask(task *iRegisteredTask) (TaskStatus, error) {
	state, err := task.State()
	if err != nil {
		return TaskStatus{}, err
	}
	n, pid, err := task.runningInstances()
	if err != nil {
		return TaskStatus{State: state}, err
	}
	return TaskStatus{State: state, Instances: n, PID: pid}, nil
}

func withRegisteredTask(name string, fn func(*iRegisteredTask) (TaskStatus, error)) (TaskStatus, error) {
	path := taskGetPath(name)
	if path == "" {
		return TaskStatus{}, fmt.Errorf("TaskName is empty")
	}
	var out TaskStatus
	err := withTaskService(func(svc *iTaskService) error {
		folder, err := svc.GetFolder(`\`)
		if err != nil {
			return fmt.Errorf("open task folder: %w", err)
		}
		defer folder.Release()
		task, err := folder.GetTask(path)
		if err != nil {
			return fmt.Errorf("open task %s: %w", name, err)
		}
		defer task.Release()
		out, err = fn(task)
		return err
	})
	return out, err
}

func taskGetPath(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", `\`)
	return strings.TrimPrefix(name, `\`)
}

var (
	taskCOMOnce sync.Once
	taskCOMWork chan func()
	taskCOMErr  error
)

// startTaskCOM pins a process-lifetime MTA thread for Task Scheduler COM.
// STA (COINIT_APARTMENTTHREADED) on a Go pool thread needs a message pump
// that Go does not run; leftover STA also hangs named-pipe Accept/Dial.
func startTaskCOM() {
	taskCOMOnce.Do(func() {
		taskCOMWork = make(chan func())
		ready := make(chan struct{})
		go func() {
			goruntime.LockOSThread()
			hr := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED)
			if hr != nil && !isCOMCode(hr, sFalse) && !isCOMCode(hr, rpcEChangedMode) {
				taskCOMErr = fmt.Errorf("CoInitializeEx: %w", hr)
				close(ready)
				return
			}
			close(ready)
			for fn := range taskCOMWork {
				fn()
			}
		}()
		<-ready
	})
}

func withTaskService(fn func(*iTaskService) error) error {
	startTaskCOM()
	if taskCOMErr != nil {
		return taskCOMErr
	}
	done := make(chan error, 1)
	taskCOMWork <- func() {
		defer func() {
			if rec := recover(); rec != nil {
				done <- fmt.Errorf("task COM panic: %v", rec)
			}
		}()
		done <- invokeTaskService(fn)
	}
	return <-done
}

func invokeTaskService(fn func(*iTaskService) error) error {
	var svc *iTaskService
	r0, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidTaskScheduler)),
		0,
		uintptr(windows.CLSCTX_INPROC_SERVER),
		uintptr(unsafe.Pointer(&iidITaskService)),
		uintptr(unsafe.Pointer(&svc)),
	)
	if err := comError(r0); err != nil {
		return fmt.Errorf("CoCreateInstance ITaskService: %w", err)
	}
	if svc == nil {
		return fmt.Errorf("CoCreateInstance ITaskService: nil")
	}
	defer svc.Release()

	var empty oleVariant
	if err := svc.Connect(&empty, &empty, &empty, &empty); err != nil {
		return fmt.Errorf("ITaskService.Connect: %w", err)
	}
	return fn(svc)
}

func isTaskAlreadyRunning(err error) bool {
	return isCOMCode(err, schedSTaskAlreadyRunning)
}

func isTaskAlreadyStopped(err error) bool {
	return isCOMCode(err, schedETaskNotRunning)
}

func isCOMCode(err error, code uint32) bool {
	if err == nil {
		return false
	}
	if errno, ok := err.(windows.Errno); ok && uint32(errno) == code {
		return true
	}
	if errno, ok := err.(syscall.Errno); ok && uint32(errno) == code {
		return true
	}
	return false
}

func comError(hr uintptr) error {
	if hr == 0 {
		return nil
	}
	if hr&0x80000000 == 0 {
		if uint32(hr) == schedSTaskAlreadyRunning {
			return windows.Errno(hr)
		}
		return nil
	}
	return windows.Errno(hr)
}

type oleVariant struct {
	VT        uint16
	reserved1 uint16
	reserved2 uint16
	reserved3 uint16
	Val       uint64
}

type bstr uintptr

func newBSTR(s string) (bstr, error) {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return 0, err
	}
	r, _, _ := procSysAllocString.Call(uintptr(unsafe.Pointer(p)))
	if r == 0 {
		return 0, fmt.Errorf("SysAllocString failed")
	}
	return bstr(r), nil
}

func (b bstr) free() {
	if b != 0 {
		procSysFreeString.Call(uintptr(b))
	}
}

type iUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

type iDispatchVtbl struct {
	iUnknownVtbl
	GetTypeInfoCount uintptr
	GetTypeInfo      uintptr
	GetIDsOfNames    uintptr
	Invoke           uintptr
}

type iTaskService struct{ vtbl *iTaskServiceVtbl }
type iTaskServiceVtbl struct {
	iDispatchVtbl
	GetFolder           uintptr
	GetRunningTasks     uintptr
	NewTask             uintptr
	Connect             uintptr
	get_Connected       uintptr
	get_TargetServer    uintptr
	get_ConnectedUser   uintptr
	get_ConnectedDomain uintptr
	get_HighestVersion  uintptr
}

type iTaskFolder struct{ vtbl *iTaskFolderVtbl }
type iTaskFolderVtbl struct {
	iDispatchVtbl
	get_Name               uintptr
	get_Path               uintptr
	GetFolder              uintptr
	GetFolders             uintptr
	CreateFolder           uintptr
	DeleteFolder           uintptr
	GetTask                uintptr
	GetTasks               uintptr
	DeleteTask             uintptr
	RegisterTask           uintptr
	RegisterTaskDefinition uintptr
	GetSecurityDescriptor  uintptr
	SetSecurityDescriptor  uintptr
}

type iRegisteredTask struct{ vtbl *iRegisteredTaskVtbl }
type iRegisteredTaskVtbl struct {
	iDispatchVtbl
	get_Name               uintptr
	get_Path               uintptr
	get_State              uintptr
	get_Enabled            uintptr
	put_Enabled            uintptr
	Run                    uintptr
	RunEx                  uintptr
	GetInstances           uintptr
	get_LastRunTime        uintptr
	get_LastTaskResult     uintptr
	get_NumberOfMissedRuns uintptr
	get_NextRunTime        uintptr
	get_Definition         uintptr
	get_Xml                uintptr
	// put_Xml is not in the IRegisteredTask vtable (Windows SDK taskschd.h).
	// An extra slot here made Stop invoke GetRunTimes with NULL SYSTEMTIME
	// pointers, which returns E_POINTER ("Invalid pointer").
	GetSecurityDescriptor uintptr
	SetSecurityDescriptor uintptr
	Stop                  uintptr
	GetRunTimes           uintptr
}

type iRunningTaskCollection struct{ vtbl *iRunningTaskCollectionVtbl }
type iRunningTaskCollectionVtbl struct {
	iDispatchVtbl
	get_Count    uintptr
	get_Item     uintptr
	get__NewEnum uintptr
}

type iRunningTask struct{ vtbl *iRunningTaskVtbl }
type iRunningTaskVtbl struct {
	iDispatchVtbl
	get_Name          uintptr
	get_InstanceGuid  uintptr
	get_Path          uintptr
	get_State         uintptr
	get_CurrentAction uintptr
	Stop              uintptr
	Refresh           uintptr
	get_EnginePID     uintptr
}

func (o *iTaskService) Release() {
	if o != nil {
		syscall.SyscallN(o.vtbl.Release, uintptr(unsafe.Pointer(o)))
	}
}

func (o *iTaskFolder) Release() {
	if o != nil {
		syscall.SyscallN(o.vtbl.Release, uintptr(unsafe.Pointer(o)))
	}
}

func (o *iRegisteredTask) Release() {
	if o != nil {
		syscall.SyscallN(o.vtbl.Release, uintptr(unsafe.Pointer(o)))
	}
}

func (o *iRunningTaskCollection) Release() {
	if o != nil {
		syscall.SyscallN(o.vtbl.Release, uintptr(unsafe.Pointer(o)))
	}
}

func (o *iRunningTask) Release() {
	if o != nil {
		syscall.SyscallN(o.vtbl.Release, uintptr(unsafe.Pointer(o)))
	}
}

func (s *iTaskService) Connect(server, user, domain, password *oleVariant) error {
	hr, _, _ := syscall.SyscallN(
		s.vtbl.Connect,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(server)),
		uintptr(unsafe.Pointer(user)),
		uintptr(unsafe.Pointer(domain)),
		uintptr(unsafe.Pointer(password)),
	)
	return comError(hr)
}

func (s *iTaskService) GetFolder(path string) (*iTaskFolder, error) {
	b, err := newBSTR(path)
	if err != nil {
		return nil, err
	}
	defer b.free()
	var folder *iTaskFolder
	hr, _, _ := syscall.SyscallN(
		s.vtbl.GetFolder,
		uintptr(unsafe.Pointer(s)),
		uintptr(b),
		uintptr(unsafe.Pointer(&folder)),
	)
	if err := comError(hr); err != nil {
		return nil, err
	}
	if folder == nil {
		return nil, fmt.Errorf("GetFolder %q: nil", path)
	}
	return folder, nil
}

func (f *iTaskFolder) GetFolder(path string) (*iTaskFolder, error) {
	b, err := newBSTR(path)
	if err != nil {
		return nil, err
	}
	defer b.free()
	var folder *iTaskFolder
	hr, _, _ := syscall.SyscallN(
		f.vtbl.GetFolder,
		uintptr(unsafe.Pointer(f)),
		uintptr(b),
		uintptr(unsafe.Pointer(&folder)),
	)
	if err := comError(hr); err != nil {
		return nil, err
	}
	if folder == nil {
		return nil, fmt.Errorf("GetFolder %q: nil", path)
	}
	return folder, nil
}

func (f *iTaskFolder) GetTask(path string) (*iRegisteredTask, error) {
	b, err := newBSTR(path)
	if err != nil {
		return nil, err
	}
	defer b.free()
	var task *iRegisteredTask
	hr, _, _ := syscall.SyscallN(
		f.vtbl.GetTask,
		uintptr(unsafe.Pointer(f)),
		uintptr(b),
		uintptr(unsafe.Pointer(&task)),
	)
	if err := comError(hr); err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("GetTask %q: nil", path)
	}
	return task, nil
}

func (f *iTaskFolder) CreateFolder(name string) (*iTaskFolder, error) {
	b, err := newBSTR(name)
	if err != nil {
		return nil, err
	}
	defer b.free()
	var empty oleVariant
	var folder *iTaskFolder
	hr, _, _ := syscall.SyscallN(
		f.vtbl.CreateFolder,
		uintptr(unsafe.Pointer(f)),
		uintptr(b),
		uintptr(unsafe.Pointer(&empty)),
		uintptr(unsafe.Pointer(&folder)),
	)
	if err := comError(hr); err != nil {
		return nil, err
	}
	if folder == nil {
		return nil, fmt.Errorf("CreateFolder %q: nil", name)
	}
	return folder, nil
}

func (f *iTaskFolder) DeleteFolder(name string) error {
	b, err := newBSTR(name)
	if err != nil {
		return err
	}
	defer b.free()
	hr, _, _ := syscall.SyscallN(
		f.vtbl.DeleteFolder,
		uintptr(unsafe.Pointer(f)),
		uintptr(b),
		0,
	)
	return comError(hr)
}

func (f *iTaskFolder) DeleteTask(name string) error {
	b, err := newBSTR(name)
	if err != nil {
		return err
	}
	defer b.free()
	hr, _, _ := syscall.SyscallN(
		f.vtbl.DeleteTask,
		uintptr(unsafe.Pointer(f)),
		uintptr(b),
		0,
	)
	return comError(hr)
}

func (f *iTaskFolder) RegisterTask(name, xml string) (*iRegisteredTask, error) {
	bName, err := newBSTR(name)
	if err != nil {
		return nil, err
	}
	defer bName.free()
	bXML, err := newBSTR(xml)
	if err != nil {
		return nil, err
	}
	defer bXML.free()
	var user, password, sddl oleVariant
	var task *iRegisteredTask
	hr, _, _ := syscall.SyscallN(
		f.vtbl.RegisterTask,
		uintptr(unsafe.Pointer(f)),
		uintptr(bName),
		uintptr(bXML),
		uintptr(taskCreateOrUpdate),
		uintptr(unsafe.Pointer(&user)),
		uintptr(unsafe.Pointer(&password)),
		uintptr(taskLogonInteractiveToken),
		uintptr(unsafe.Pointer(&sddl)),
		uintptr(unsafe.Pointer(&task)),
	)
	if err := comError(hr); err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("RegisterTask %q: nil", name)
	}
	return task, nil
}

func (t *iRegisteredTask) State() (TaskState, error) {
	var state uint32
	hr, _, _ := syscall.SyscallN(
		t.vtbl.get_State,
		uintptr(unsafe.Pointer(t)),
		uintptr(unsafe.Pointer(&state)),
	)
	if err := comError(hr); err != nil {
		return 0, err
	}
	return TaskState(state), nil
}

func (t *iRegisteredTask) Run() error {
	var params oleVariant
	var running *iRunningTask
	hr, _, _ := syscall.SyscallN(
		t.vtbl.Run,
		uintptr(unsafe.Pointer(t)),
		uintptr(unsafe.Pointer(&params)),
		uintptr(unsafe.Pointer(&running)),
	)
	if running != nil {
		running.Release()
	}
	if uint32(hr) == schedSTaskAlreadyRunning {
		return windows.Errno(schedSTaskAlreadyRunning)
	}
	return comError(hr)
}

func (t *iRegisteredTask) Stop() error {
	hr, _, _ := syscall.SyscallN(
		t.vtbl.Stop,
		uintptr(unsafe.Pointer(t)),
		0,
	)
	if uint32(hr) == schedETaskNotRunning {
		return windows.Errno(schedETaskNotRunning)
	}
	return comError(hr)
}

func (t *iRegisteredTask) runningInstances() (int, int, error) {
	var coll *iRunningTaskCollection
	hr, _, _ := syscall.SyscallN(
		t.vtbl.GetInstances,
		uintptr(unsafe.Pointer(t)),
		0,
		uintptr(unsafe.Pointer(&coll)),
	)
	if err := comError(hr); err != nil {
		return 0, 0, err
	}
	if coll == nil {
		return 0, 0, nil
	}
	defer coll.Release()
	var count int32
	hr, _, _ = syscall.SyscallN(
		coll.vtbl.get_Count,
		uintptr(unsafe.Pointer(coll)),
		uintptr(unsafe.Pointer(&count)),
	)
	if err := comError(hr); err != nil {
		return 0, 0, err
	}
	if count <= 0 {
		return 0, 0, nil
	}
	idx := oleVariant{VT: vtI4}
	*(*int32)(unsafe.Pointer(&idx.Val)) = 1
	var inst *iRunningTask
	hr, _, _ = syscall.SyscallN(
		coll.vtbl.get_Item,
		uintptr(unsafe.Pointer(coll)),
		uintptr(unsafe.Pointer(&idx)),
		uintptr(unsafe.Pointer(&inst)),
	)
	if err := comError(hr); err != nil {
		return int(count), 0, nil
	}
	if inst == nil {
		return int(count), 0, nil
	}
	defer inst.Release()
	var pid uint32
	hr, _, _ = syscall.SyscallN(
		inst.vtbl.get_EnginePID,
		uintptr(unsafe.Pointer(inst)),
		uintptr(unsafe.Pointer(&pid)),
	)
	if err := comError(hr); err != nil {
		return int(count), 0, nil
	}
	return int(count), int(pid), nil
}

// ThrowawayKeepAliveExec is ping -t for RegisterThrowawayTask. Tests must not
// re-exec the test binary: Task Scheduler sometimes puts Arguments in argv[0],
// TestMain missed -winunitd-helper=, and the child re-ran the full suite
// (named-pipe fights and notify Start hangs).
func ThrowawayKeepAliveExec() (exe, args string) {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return root + `\System32\ping.exe`, "-t 127.0.0.1"
}

// RegisterThrowawayTask creates an isolated on-demand task for Windows tests.
// Type=scheduled-task production start/stop/status never calls this.
func RegisterThrowawayTask(folder, name, exe, args string) (string, error) {
	full := `\` + folder + `\` + name
	err := withTaskService(func(svc *iTaskService) error {
		root, err := svc.GetFolder(`\`)
		if err != nil {
			return err
		}
		defer root.Release()
		sub, err := root.GetFolder(folder)
		if err != nil {
			created, cerr := root.CreateFolder(folder)
			if cerr != nil {
				return fmt.Errorf("CreateFolder %s: %w", folder, cerr)
			}
			sub = created
		}
		defer sub.Release()
		xml := throwawayTaskXML(exe, args)
		task, err := sub.RegisterTask(name, xml)
		if err != nil {
			return fmt.Errorf("RegisterTask %s: %w", full, err)
		}
		task.Release()
		return nil
	})
	return full, err
}

func DeleteThrowawayTask(folder, name string) error {
	return withTaskService(func(svc *iTaskService) error {
		sub, err := svc.GetFolder(`\` + folder)
		if err != nil {
			return nil
		}
		defer sub.Release()
		if task, err := sub.GetTask(name); err == nil {
			_ = task.Stop()
			task.Release()
		}
		_ = sub.DeleteTask(name)
		root, err := svc.GetFolder(`\`)
		if err != nil {
			return nil
		}
		defer root.Release()
		_ = root.DeleteFolder(folder)
		return nil
	})
}

func throwawayTaskXML(exe, args string) string {
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>winunitd TS1 throwaway test task; created and deleted by the test</Description>
  </RegistrationInfo>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>true</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(exe) + `</Command>
      <Arguments>` + xmlEscape(args) + `</Arguments>
    </Exec>
  </Actions>
</Task>`
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}
