package runtime

import (
	"errors"
	"fmt"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/PLN/winunitd/internal/unit"
	"golang.org/x/sys/windows"
)

var admissionProbeSlots = make(chan struct{}, 4)

type admissionProbeHandle struct {
	handle windows.Handle
	find   bool
}

func (h admissionProbeHandle) close() error {
	if h.find {
		return windows.FindClose(h.handle)
	}
	return windows.CloseHandle(h.handle)
}

type admissionProbeResult struct {
	present bool
	err     error
}

// UserUnitFilesPresent probes only as the supplied user. A timed-out worker
// retains its duplicated token and admission slot until native I/O completes.
func UserUnitFilesPresent(user *UserToken) (bool, error) {
	return userUnitFilesPresent(user, func(tok windows.Token) (string, error) {
		return tok.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DONT_VERIFY)
	}, 5*time.Second)
}

func userUnitFilesPresent(user *UserToken, resolve func(windows.Token) (string, error), timeout time.Duration) (bool, error) {
	tok, ok := nativeToken(user)
	if !ok {
		return false, ErrNoUserToken
	}
	select {
	case admissionProbeSlots <- struct{}{}:
	default:
		return false, fmt.Errorf("user admission probe capacity exhausted")
	}
	var duplicate windows.Token
	if err := windows.DuplicateTokenEx(tok, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE|windows.TOKEN_DUPLICATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &duplicate); err != nil {
		if duplicate != 0 {
			user.cleanup = append(user.cleanup, winToken(duplicate))
		}
		<-admissionProbeSlots
		return false, err
	}
	done := make(chan struct{})
	// A deadline only ends the probe's caller wait. Token cleanup also joins
	// the owned worker, so user-host shutdown cannot overlook its file handles.
	user.cleanup = append(user.cleanup, tokenCleanupFunc(func() error { <-done; return nil }))
	result := make(chan admissionProbeResult, 1)
	go func() {
		defer close(done)
		defer func() { <-admissionProbeSlots }()
		goruntime.LockOSThread()
		unlock := true
		defer func() {
			if unlock {
				goruntime.UnlockOSThread()
			}
		}()
		var handles []admissionProbeHandle
		present, err := probeUserUnitFiles(duplicate, &handles, resolve)
		if revertErr := windows.RevertToSelf(); revertErr != nil {
			// Exit this dedicated goroutine with its thread still locked so
			// the Go runtime destroys the thread instead of reusing its token.
			unlock = false
			present = false
			err = errors.Join(err, revertErr)
		}
		responded := false
		for {
			var pending []admissionProbeHandle
			var closeErr error
			for _, h := range handles {
				if e := h.close(); e != nil {
					pending = append(pending, h)
					closeErr = errors.Join(closeErr, e)
				}
			}
			handles = pending
			if duplicate != 0 {
				if e := duplicate.Close(); e != nil {
					closeErr = errors.Join(closeErr, e)
				} else {
					duplicate = 0
				}
			}
			if closeErr == nil {
				if !responded {
					result <- admissionProbeResult{present, err}
				}
				return
			}
			if !responded {
				result <- admissionProbeResult{false, errors.Join(err, closeErr)}
				responded = true
			}
			time.Sleep(time.Second)
		}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-result:
		return r.present, r.err
	case <-timer.C:
		return false, fmt.Errorf("user admission probe timed out")
	}
}

func probeUserUnitFiles(tok windows.Token, handles *[]admissionProbeHandle, resolve func(windows.Token) (string, error)) (bool, error) {
	if err := windows.SetThreadToken(nil, tok); err != nil {
		return false, err
	}
	base, err := resolve(tok)
	if err != nil {
		return false, err
	}
	return probeUnitDirectory(filepath.Join(base, "winunitd", "units"), handles)
}

// Pin every directory without write/delete sharing while traversing, rejecting
// reparse points. Prevent both rename and in-place reparse changes during use.
func probeUnitDirectory(dir string, handles *[]admissionProbeHandle) (bool, error) {
	volume := filepath.VolumeName(dir)
	if len(volume) != 2 || volume[1] != ':' || !filepath.IsAbs(dir) {
		return false, fmt.Errorf("delegated admission requires a local unit directory")
	}
	current := volume + `\`
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(dir), current), `\`)
	paths := []string{current}
	for _, part := range parts {
		current = filepath.Join(current, part)
		paths = append(paths, current)
	}
	for _, path := range paths {
		p, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return false, err
		}
		h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		*handles = append(*handles, admissionProbeHandle{handle: h})
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(h, &info); err != nil {
			return false, err
		}
		if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			return false, fmt.Errorf("unit directory contains a reparse point or non-directory")
		}
	}
	pattern, err := windows.UTF16PtrFromString(filepath.Join(dir, "*"))
	if err != nil {
		return false, err
	}
	var data windows.Win32finddata
	find, err := windows.FindFirstFile(pattern, &data)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	*handles = append(*handles, admissionProbeHandle{handle: find, find: true})
	for scanned := 0; scanned < 4096; scanned++ {
		if data.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) == 0 {
			if _, err := unit.KindFromName(windows.UTF16ToString(data.FileName[:])); err == nil {
				return true, nil
			}
		}
		err := windows.FindNextFile(find, &data)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	return false, fmt.Errorf("user unit directory exceeds admission scan limit")
}
