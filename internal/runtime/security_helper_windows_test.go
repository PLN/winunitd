//go:build windows

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/PLN/winunitd/internal/protocol"
	"golang.org/x/sys/windows"
)

// NativeSecurityReport is test-only evidence from the actual child token and
// handle table. File identity, rather than handle-number validity, distinguishes
// inherited sentinels from unrelated handles allocated at the same numbers.
type NativeSecurityReport struct {
	PID           uint32
	SID           string
	Session       uint32
	ElevationType uint32
	Elevated      uint32
	AdminFlags    uint32
	Files         []NativeFileIdentity
	BrokerEnv     bool
	DefaultStdio  bool
	WritableStdio bool
	Folders       bool
	Denied        []bool
}

type NativeFileIdentity struct {
	Volume uint32
	High   uint32
	Low    uint32
}

func NativeHandleFileIdentity(h windows.Handle) NativeFileIdentity {
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(h, &info) != nil {
		return NativeFileIdentity{}
	}
	return NativeFileIdentity{info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow}
}

func runSecurityReportHelper() error {
	args := make(map[string]string)
	for _, arg := range os.Args {
		if key, value, ok := strings.Cut(arg, "="); ok {
			args[key] = value
		}
	}
	r := NativeSecurityReport{PID: windows.GetCurrentProcessId()}
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return err
	}
	r.SID = u.User.Sid.String()
	for class, value := range map[uint32]*uint32{
		windows.TokenSessionId: &r.Session, windows.TokenElevationType: &r.ElevationType, windows.TokenElevation: &r.Elevated,
	} {
		var returned uint32
		if err := windows.GetTokenInformation(tok, class, (*byte)(unsafe.Pointer(value)), 4, &returned); err != nil {
			return err
		}
	}
	groups, err := tok.GetTokenGroups()
	if err != nil {
		return err
	}
	for _, group := range groups.AllGroups() {
		if group.Sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
			r.AdminFlags = group.Attributes
		}
	}
	for _, value := range strings.Split(args["--sentinel-handles"], ",") {
		h, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return err
		}
		r.Files = append(r.Files, NativeHandleFileIdentity(windows.Handle(h)))
	}
	_, r.BrokerEnv = os.LookupEnv("WINUNITD_SECURITY_BROKER_ONLY")
	var startup windows.StartupInfo
	r.DefaultStdio = windows.GetStartupInfo(&startup) == nil && startup.Flags&windows.STARTF_USESTDHANDLES == 0
	r.WritableStdio = writeStdHandle(windows.STD_OUTPUT_HANDLE, []byte("security-stdout\n")) == nil &&
		writeStdHandle(windows.STD_ERROR_HANDLE, []byte("security-stderr\n")) == nil
	info, err := CurrentUserInfo()
	r.Folders = err == nil && info.LocalAppData != "" && info.RoamingAppData != "" &&
		os.Getenv("LOCALAPPDATA") == info.LocalAppData && os.Getenv("APPDATA") == info.RoamingAppData
	if denied := args["--denied-pipes"]; denied != "" {
		for _, name := range strings.Split(denied, ",") {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			c, err := protocol.DialPipe(ctx, name)
			cancel()
			if c != nil {
				c.Close()
			}
			// Timeout or a missing server is not evidence of access rejection.
			r.Denied = append(r.Denied, os.IsPermission(err))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := protocol.DialPipe(ctx, args["--security-report-pipe"])
	if err != nil {
		return fmt.Errorf("report pipe: %w", err)
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if err := json.NewEncoder(c).Encode(r); err != nil {
		return err
	}
	// Keep the caller alive until the parent authenticates its real token.
	var ack [1]byte
	_, err = c.Read(ack[:])
	return err
}
