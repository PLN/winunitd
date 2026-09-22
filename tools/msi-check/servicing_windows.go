//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// savedServiceState is one MSI transaction. MSI owns file rollback; this
// record owns the prior SCM configuration and whether the service was running.
type savedServiceState struct {
	Version     int
	Transaction string
	Running     bool
	Policy      savedPolicy
}

func serviceTransaction(args []string) error {
	if len(args) != 4 || !validServiceToken(args[3]) {
		return fmt.Errorf("invalid service transaction arguments")
	}
	op, token := args[0], args[3]
	switch op {
	case "service-prepare", "service-rollback", "service-commit":
	default:
		return fmt.Errorf("unknown service operation")
	}
	statePath, err := serviceStatePath(token)
	if err != nil {
		return err
	}
	if op != "service-prepare" {
		saved, err := readServiceState(statePath, token)
		if errors.Is(err, os.ErrNotExist) {
			return nil // Prepare may not have run before rollback.
		}
		if err != nil {
			return err
		}
		if err := requireStandardDirs(args[1], args[2]); err != nil {
			return err
		}
		if op == "service-rollback" && saved.Policy.Exists {
			if err := rollbackService(saved); err != nil {
				return err
			}
		}
		err = os.Remove(statePath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := requireStandardDirs(args[1], args[2]); err != nil {
		return err
	}
	installDir, dataDir, err := standardProductDirs()
	if err != nil {
		return err
	}
	for _, dir := range []string{installDir, dataDir} {
		if err := protectedDirectory(dir); err != nil {
			return err
		}
	}
	return prepareService(statePath, token, installDir, dataDir)
}

func standardProductDirs() (installDir, dataDir string, err error) {
	programFiles, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFilesX64, 0)
	if err != nil {
		return "", "", err
	}
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(programFiles, "winunitd"), filepath.Join(programData, "winunitd"), nil
}

func requireStandardDirs(installArg, dataArg string) error {
	installDir, dataDir, err := standardProductDirs()
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(installArg), installDir) || !strings.EqualFold(filepath.Clean(dataArg), dataDir) {
		return fmt.Errorf("product servicing requires the standard Program Files and ProgramData directories")
	}
	return nil
}

func serviceStatePath(token string) (string, error) {
	base, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFilesX64, 0)
	if err != nil {
		return "", err
	}
	// The state file sits in Program Files, outside the product directory,
	// so old-product removal cannot delete the record rollback still needs.
	// The parent ACL limits writers to SYSTEM and Administrators; the file
	// descriptor below tightens that again.
	return filepath.Join(base, "winunitd-service-"+token+".json"), nil
}

func prepareService(statePath, token, installDir, dataDir string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(runtime.ServiceName)
	saved := savedServiceState{Version: serviceStateVersion, Transaction: token}
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		if err := writeServiceState(statePath, saved); err != nil {
			return err
		}
		return abortIfPayloadBusy(installDir)
	}
	if err != nil {
		return err
	}
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return err
	}
	if err := productServiceIdentity(cfg, installDir, dataDir); err != nil {
		return err
	}
	st, err := s.Query()
	if err != nil {
		return err
	}
	quiesce, err := planServiceStop(serviceStateName(st.State))
	if err != nil {
		return err
	}
	saved.Running = st.State == svc.Running
	saved.Policy.Version = 1
	saved.Policy.Transaction = token
	if err := capturePolicy(s, &saved.Policy); err != nil {
		return err
	}
	// Persist before quiesce or stop so a failed stop can restore this state.
	if err := writeServiceState(statePath, saved); err != nil {
		return err
	}
	if quiesce {
		if err := quiesceManager(context.Background()); err != nil {
			return err
		}
		st, err = s.Query()
		if err != nil {
			return err
		}
	}
	if err := stopForReplacement(s, st); err != nil {
		return err
	}
	return abortIfPayloadBusy(installDir)
}

func abortIfPayloadBusy(installDir string) error {
	locked, err := lockedPayloads(installDir)
	if err != nil {
		return err
	}
	return replacementBlocked(false, locked)
}

func productServiceIdentity(cfg mgr.Config, installDir, dataDir string) error {
	argv, err := windows.DecomposeCommandLine(cfg.BinaryPathName)
	exe := filepath.Join(installDir, "bin", "winunitd.exe")
	if err != nil || len(argv) != 3 || !strings.EqualFold(filepath.Clean(argv[0]), exe) || argv[1] != "--base-dir" || !strings.EqualFold(filepath.Clean(argv[2]), dataDir) || !localSystemAccount(cfg.ServiceStartName) {
		return fmt.Errorf("service registration differs from this package; restore it before servicing")
	}
	return nil
}

func localSystemAccount(name string) bool {
	return strings.EqualFold(name, "LocalSystem") || strings.EqualFold(name, `NT AUTHORITY\SYSTEM`)
}

func serviceStateName(state svc.State) string {
	switch state {
	case svc.Running:
		return "running"
	case svc.Stopped:
		return "stopped"
	default:
		return "transition"
	}
}

func quiesceManager(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	conn, err := protocol.DialMaintenance(dialCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("abort replacement: maintenance endpoint unavailable: %w", err)
	}
	defer conn.Close()
	// Read deadlines on the maintenance pipe are cleared while the handler
	// runs. Bound this side so a stuck manager cannot hold the transaction
	// open past the quiesce budget.
	if err := conn.SetDeadline(time.Now().Add(quiesceBudget + 15*time.Second)); err != nil {
		return fmt.Errorf("abort replacement: maintenance did not quiesce: %w", err)
	}
	result, err := protocol.NewClient(conn).Maintenance(ctx, protocol.MaintenanceParams{
		TimeoutMS: int64(quiesceBudget / time.Millisecond),
	})
	state := ""
	if result != nil {
		state = result.State
	}
	return requireQuiesced(state, err)
}

func stopForReplacement(s *mgr.Service, st svc.Status) error {
	deadline := time.Now().Add(serviceStopBudget)
	pid := st.ProcessId
	if st.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			cur, qerr := s.Query()
			if qerr != nil || cur.State != svc.Stopped {
				return fmt.Errorf("abort replacement: stop failed: %w", err)
			}
		}
		if err := waitServiceState(s, svc.Stopped, deadline); err != nil {
			return fmt.Errorf("abort replacement: stop exceeded %s", serviceStopBudget)
		}
	}
	if err := waitProcessExit(pid, deadline); err != nil {
		return err
	}
	// SCM can report the old process id briefly after the process has exited.
	clearBy := deadline
	if time.Until(clearBy) < 5*time.Second {
		clearBy = time.Now().Add(5 * time.Second)
	}
	for {
		cur, err := s.Query()
		if err != nil {
			return err
		}
		if cur.State == svc.Stopped && cur.ProcessId == 0 {
			return nil
		}
		if !time.Now().Before(clearBy) {
			return replacementBlocked(true, nil)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func waitProcessExit(pid uint32, deadline time.Time) error {
	if pid == 0 {
		return nil
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		// The id is already gone. A live id that cannot be opened still aborts.
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return fmt.Errorf("abort replacement: cannot confirm service process exit: %w", err)
	}
	defer windows.CloseHandle(handle)
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return replacementBlocked(true, nil)
	}
	result, err := windows.WaitForSingleObject(handle, uint32(remaining/time.Millisecond))
	if err != nil {
		return err
	}
	if result != windows.WAIT_OBJECT_0 {
		return replacementBlocked(true, nil)
	}
	return nil
}

func waitServiceState(s *mgr.Service, want svc.State, deadline time.Time) error {
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("service state deadline expired")
}

func lockedPayloads(installDir string) ([]string, error) {
	var locked []string
	for _, name := range []string{"winunitd.exe", "winctl.exe", "winunit-notify.exe"} {
		inUse, err := payloadInUse(filepath.Join(installDir, "bin", name))
		if err != nil {
			return nil, err
		}
		if inUse {
			locked = append(locked, name)
		}
	}
	return locked, nil
}

// payloadInUse is true when an exclusive open fails because another handle
// still has the file. A missing file is not in use.
func payloadInUse(path string) (bool, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attrs, err := windows.GetFileAttributes(name)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return false, fmt.Errorf("abort replacement: payload is not an ordinary file")
	}
	// Share mode 0 asks for no concurrent readers or writers.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, windows.CloseHandle(handle)
}

func rollbackService(saved savedServiceState) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(runtime.ServiceName)
	if err != nil {
		return err
	}
	defer s.Close()
	// Stop whatever MSI restarted before rewriting the saved policy, then
	// return the service to the running state captured before replacement.
	if err := ensureStopped(s); err != nil {
		return err
	}
	if err := restorePolicy(s, saved.Policy); err != nil {
		return err
	}
	if !saved.Running {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State != svc.Stopped {
			return fmt.Errorf("rollback did not restore stopped state")
		}
		return nil
	}
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return err
	}
	if err := waitServiceState(s, svc.Running, time.Now().Add(serviceStopBudget)); err != nil {
		return fmt.Errorf("rollback did not restore running state: %w", err)
	}
	return nil
}

func ensureStopped(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped && st.ProcessId == 0 {
		return nil
	}
	deadline := time.Now().Add(serviceStopBudget)
	if st.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			cur, qerr := s.Query()
			if qerr != nil || cur.State != svc.Stopped {
				return err
			}
		}
		if err := waitServiceState(s, svc.Stopped, deadline); err != nil {
			return fmt.Errorf("rollback stop exceeded %s", serviceStopBudget)
		}
	}
	cur, err := s.Query()
	if err != nil {
		return err
	}
	return waitProcessExit(cur.ProcessId, deadline)
}

func writeServiceState(path string, saved savedServiceState) error {
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if len(data) > maxServiceStateBytes || len(saved.Policy.Actions) > 64 {
		return fmt.Errorf("existing service configuration exceeds rollback bounds")
	}
	f, err := createPolicyState(path)
	if err != nil {
		return fmt.Errorf("create exclusive service state: %w", err)
	}
	_, writeErr := f.Write(data)
	return errors.Join(writeErr, f.Sync(), f.Close())
}

func readServiceState(path, token string) (savedServiceState, error) {
	var saved savedServiceState
	info, err := os.Lstat(path)
	if err != nil {
		return saved, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxServiceStateBytes {
		return saved, fmt.Errorf("invalid service state file")
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return saved, err
	}
	if err := protectedSecurity(sd); err != nil {
		return saved, err
	}
	f, err := os.Open(path)
	if err != nil {
		return saved, err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxServiceStateBytes+1))
	if err := errors.Join(readErr, f.Close()); err != nil {
		return saved, err
	}
	if len(data) > maxServiceStateBytes {
		return saved, fmt.Errorf("service state exceeds limit")
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return saved, err
	}
	if saved.Version != serviceStateVersion || saved.Transaction != token || len(saved.Policy.Actions) > 64 {
		return saved, fmt.Errorf("service state identity or version mismatch")
	}
	return saved, nil
}
