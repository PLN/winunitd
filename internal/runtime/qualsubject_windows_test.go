//go:build windows

package runtime_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// qualSubjectEnv names a file a qualification runner asks a native test to
// write: the token of the S4U subject process the test launched, so the
// runner's receipt can show which account and logon the test exercised.
const qualSubjectEnv = "WINUNITD_QUAL_SUBJECT_OUT"

var (
	qualSecur32                = windows.NewLazySystemDLL("secur32.dll")
	qualLsaGetLogonSessionData = qualSecur32.NewProc("LsaGetLogonSessionData")
	qualLsaFreeReturnBuffer    = qualSecur32.NewProc("LsaFreeReturnBuffer")
)

// qualReport has the fields and JSON names of the qualification
// receipt's subject report: this test, this test process's incarnation and
// the subject process's incarnation and token.
type qualReport struct {
	Test         string      `json:"test"`
	OwnerPID     uint32      `json:"ownerPid"`
	OwnerCreated uint64      `json:"ownerCreated"`
	PID          uint32      `json:"pid"`
	Created      uint64      `json:"created"`
	Token        qualSubject `json:"token"`
}

// qualSubject has the fields and JSON names of the qualification
// receipt's token facts.
type qualSubject struct {
	SID              string `json:"sid"`
	Session          uint32 `json:"session"`
	Elevated         bool   `json:"elevated"`
	Source           string `json:"source"`
	LogonType        uint32 `json:"logonType,omitempty"`
	AuthPackage      string `json:"authPackage,omitempty"`
	AuthenticationID string `json:"authenticationId"`
	ElevationType    uint32 `json:"elevationType,omitempty"`
}

// reportQualSubject writes the launched subject's report when the runner
// set qualSubjectEnv; otherwise it does nothing. Failures fail the test: a
// requested report that is missing would leave the run unproven.
func reportQualSubject(t *testing.T, process windows.Handle) {
	t.Helper()
	path := os.Getenv(qualSubjectEnv)
	if path == "" {
		return
	}
	s, err := subjectFacts(process)
	if err != nil {
		t.Fatalf("qualification subject: %v", err)
	}
	r := qualReport{Test: t.Name(), OwnerPID: windows.GetCurrentProcessId(), Token: s}
	if r.OwnerCreated, err = processCreated(windows.CurrentProcess()); err == nil {
		if r.PID, err = windows.GetProcessId(process); err == nil {
			r.Created, err = processCreated(process)
		}
	}
	if err != nil {
		t.Fatalf("qualification subject identity: %v", err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal("qualification subject report could not be written")
	}
}

func subjectFacts(process windows.Handle) (qualSubject, error) {
	var s qualSubject
	var tok windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY|windows.TOKEN_QUERY_SOURCE, &tok); err != nil {
		return s, err
	}
	defer tok.Close()
	user, err := tok.GetTokenUser()
	if err != nil {
		return s, err
	}
	s.SID = user.User.Sid.String()
	s.Elevated = tok.IsElevated()
	var n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&s.Session)), 4, &n); err != nil {
		return s, err
	}
	if err := windows.GetTokenInformation(tok, windows.TokenElevationType, (*byte)(unsafe.Pointer(&s.ElevationType)), 4, &n); err != nil {
		return s, err
	}
	var source struct {
		Name [8]byte
		ID   windows.LUID
	}
	if err := windows.GetTokenInformation(tok, windows.TokenSource, (*byte)(unsafe.Pointer(&source)), uint32(unsafe.Sizeof(source)), &n); err != nil {
		return s, err
	}
	s.Source = strings.TrimRight(string(source.Name[:]), "\x00 ")
	var stats struct {
		TokenID            windows.LUID
		AuthenticationID   windows.LUID
		ExpirationTime     int64
		TokenType          uint32
		ImpersonationLevel uint32
		DynamicCharged     uint32
		DynamicAvailable   uint32
		GroupCount         uint32
		PrivilegeCount     uint32
		ModifiedID         windows.LUID
	}
	if err := windows.GetTokenInformation(tok, windows.TokenStatistics, (*byte)(unsafe.Pointer(&stats)), uint32(unsafe.Sizeof(stats)), &n); err != nil {
		return s, err
	}
	id := stats.AuthenticationID
	s.AuthenticationID = fmt.Sprintf("%08x:%08x", id.HighPart, id.LowPart)
	var data *struct {
		Size                  uint32
		LogonID               windows.LUID
		UserName              windows.NTUnicodeString
		LogonDomain           windows.NTUnicodeString
		AuthenticationPackage windows.NTUnicodeString
		LogonType             uint32
	}
	if r, _, _ := qualLsaGetLogonSessionData.Call(uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&data))); r != 0 || data == nil {
		return s, fmt.Errorf("logon session data: status %#x", r)
	}
	s.LogonType, s.AuthPackage = data.LogonType, data.AuthenticationPackage.String()
	qualLsaFreeReturnBuffer.Call(uintptr(unsafe.Pointer(data)))
	return s, nil
}

func processCreated(process windows.Handle) (uint64, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), nil
}
