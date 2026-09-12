//go:build windows

package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	goruntime "runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/PLN/winunitd/internal/protocol"
	"golang.org/x/sys/windows"
)

const userDesktopPrefix = "winunitd-headless-"

// The profile may first be created by CreateProcessWithTokenW, so a headless
// manager selects its working directory after Windows has loaded that profile.
func SetHeadlessUserWorkingDirectory(profile string) error {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		return err
	}
	if session == 0 {
		return os.Chdir(profile)
	}
	return nil
}

func validUserDesktopName(name string) bool {
	if !strings.HasPrefix(name, userDesktopPrefix) {
		return false
	}
	value := strings.TrimPrefix(name, userDesktopPrefix)
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && value == strings.ToLower(value)
}

func startUserDesktop(spec UserManagerSpec) (*userDesktopLease, string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, "", err
	}
	name := userDesktopPrefix + hex.EncodeToString(nonce[:])
	helper, err := NewLauncher(spec.Daemon).Start(context.Background(), StartSpec{
		Unit: "user-desktop-helper",
		Argv: []string{spec.Exe, UserDesktopHelperFlag, spec.SID, name},
	})
	if helper == nil {
		return nil, "", err
	}
	lease := &userDesktopLease{helper: helper}
	if err != nil {
		return lease, "", err
	}
	path := name + `\default`
	if err := lease.readReady(path+"\n", 5*time.Second); err != nil {
		return lease, "", err
	}
	return lease, path, nil
}

// ServeUserDesktopHelper runs only in an isolated SYSTEM process. Changing a
// process window station in the multithreaded broker is forbidden. This process
// performs no manager initialization, launches no workloads and owns no profile.
func ServeUserDesktopHelper(sid, name string, stdout io.Writer) error {
	if !runningAsLocalSystem() {
		return errors.New("SYSTEM required")
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		return err
	}
	if session != 0 || !protocol.ValidSID(sid) || !validUserDesktopName(name) {
		return errors.New("invalid desktop helper identity or name")
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;" + sid + ")")
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	user32 := windows.NewLazySystemDLL("user32.dll")
	namep, _ := windows.UTF16PtrFromString(name)
	// CWF_CREATE_ONLY rejects an existing station instead of trusting its ACL.
	station, _, err := user32.NewProc("CreateWindowStationW").Call(uintptr(unsafe.Pointer(namep)), 1, 0xF037F, uintptr(unsafe.Pointer(&sa)))
	if station == 0 {
		return fmt.Errorf("create private window station: %w", err)
	}
	defer user32.NewProc("CloseWindowStation").Call(station)
	previous, _, err := user32.NewProc("GetProcessWindowStation").Call()
	if previous == 0 {
		return err
	}
	selected, _, err := user32.NewProc("SetProcessWindowStation").Call(station)
	if selected == 0 {
		return fmt.Errorf("select private station: %w", err)
	}
	desktopName, _ := windows.UTF16PtrFromString("default")
	desktop, _, createErr := user32.NewProc("CreateDesktopW").Call(uintptr(unsafe.Pointer(desktopName)), 0, 0, 0, 0xF01FF, uintptr(unsafe.Pointer(&sa)))
	goruntime.KeepAlive(namep)
	goruntime.KeepAlive(desktopName)
	goruntime.KeepAlive(descriptor)
	goruntime.KeepAlive(sa)
	restored, _, restoreErr := user32.NewProc("SetProcessWindowStation").Call(previous)
	if desktop != 0 {
		defer user32.NewProc("CloseDesktop").Call(desktop)
	}
	if restored == 0 {
		return fmt.Errorf("restore helper station: %w", restoreErr)
	}
	if desktop == 0 {
		return fmt.Errorf("create private desktop: %w", createErr)
	}
	if _, err := io.WriteString(stdout, name+"\\default\n"); err != nil {
		return err
	}
	// The parent owns this process through nested kill-on-close jobs. Holding
	// the USER handles keeps the named desktop available throughout launch.
	for {
		time.Sleep(time.Hour)
	}
}
