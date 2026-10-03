//go:build windows

package headless

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	secur32                    = windows.NewLazySystemDLL("secur32.dll")
	procLsaGetLogonSessionData = secur32.NewProc("LsaGetLogonSessionData")
	procLsaFreeReturnBuffer    = secur32.NewProc("LsaFreeReturnBuffer")
	advapi32                   = windows.NewLazySystemDLL("advapi32.dll")
	procLookupPrivilegeNameW   = advapi32.NewProc("LookupPrivilegeNameW")
)

// logonSessionData is the head of SECURITY_LOGON_SESSION_DATA; only these
// fields are read.
type logonSessionData struct {
	Size                  uint32
	LogonID               windows.LUID
	UserName              windows.NTUnicodeString
	LogonDomain           windows.NTUnicodeString
	AuthenticationPackage windows.NTUnicodeString
	LogonType             uint32
}

// tokenStatistics is TOKEN_STATISTICS.
type tokenStatistics struct {
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

// ProbeOwnToken describes this process's own primary token and the profile
// folders it resolves from that token.
func ProbeOwnToken() (TokenProbe, error) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE|windows.TOKEN_DUPLICATE, &tok); err != nil {
		return TokenProbe{}, fmt.Errorf("open own token: %w", err)
	}
	defer tok.Close()
	var p TokenProbe
	user, err := tok.GetTokenUser()
	if err != nil {
		return p, err
	}
	p.SID = user.User.Sid.String()
	var stats tokenStatistics
	var n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenStatistics, (*byte)(unsafe.Pointer(&stats)), uint32(unsafe.Sizeof(stats)), &n); err != nil {
		return p, fmt.Errorf("token statistics: %w", err)
	}
	p.AuthenticationID = fmt.Sprintf("%08x:%08x", stats.AuthenticationID.HighPart, stats.AuthenticationID.LowPart)
	if err := windows.GetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&p.Session)), 4, &n); err != nil {
		return p, fmt.Errorf("token session: %w", err)
	}
	if err := windows.GetTokenInformation(tok, windows.TokenElevationType, (*byte)(unsafe.Pointer(&p.ElevationType)), 4, &n); err != nil {
		return p, fmt.Errorf("token elevation type: %w", err)
	}
	p.Elevated = tok.IsElevated()
	p.Integrity, err = integrity(tok)
	if err != nil {
		return p, err
	}
	groups, err := tok.GetTokenGroups()
	if err != nil {
		return p, err
	}
	for _, g := range groups.AllGroups() {
		p.Groups = append(p.Groups, TokenGroup{SID: g.Sid.String(), Attributes: g.Attributes})
	}
	if p.Privileges, err = privileges(tok); err != nil {
		return p, err
	}
	p.LogonType, p.AuthPackage, p.LogonSessionError = logonSession(stats.AuthenticationID)
	p.KnownFolders, p.KnownFolderErrors = map[string]string{}, map[string]uint32{}
	for name, id := range map[string]*windows.KNOWNFOLDERID{
		"profile": windows.FOLDERID_Profile, "localAppData": windows.FOLDERID_LocalAppData, "roamingAppData": windows.FOLDERID_RoamingAppData,
	} {
		path, err := tok.KnownFolderPath(id, windows.KF_FLAG_DONT_VERIFY)
		if err != nil {
			p.KnownFolderErrors[name] = win32Code(err)
			continue
		}
		p.KnownFolders[name] = path
	}
	return p, nil
}

func integrity(tok windows.Token) (string, error) {
	var n uint32
	_ = windows.GetTokenInformation(tok, windows.TokenIntegrityLevel, nil, 0, &n)
	if n == 0 {
		return "", errors.New("token integrity size")
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(tok, windows.TokenIntegrityLevel, &buf[0], n, &n); err != nil {
		return "", fmt.Errorf("token integrity: %w", err)
	}
	label := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&buf[0]))
	sid := label.Label.Sid
	rid := sid.SubAuthority(uint32(sid.SubAuthorityCount()) - 1)
	switch {
	case rid >= 0x4000:
		return "system", nil
	case rid >= 0x3000:
		return "high", nil
	case rid >= 0x2000:
		return "medium", nil
	case rid >= 0x1000:
		return "low", nil
	}
	return "untrusted", nil
}

func privileges(tok windows.Token) ([]TokenPrivilege, error) {
	var n uint32
	_ = windows.GetTokenInformation(tok, windows.TokenPrivileges, nil, 0, &n)
	if n == 0 {
		return nil, errors.New("token privileges size")
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(tok, windows.TokenPrivileges, &buf[0], n, &n); err != nil {
		return nil, fmt.Errorf("token privileges: %w", err)
	}
	var out []TokenPrivilege
	for _, p := range (*windows.Tokenprivileges)(unsafe.Pointer(&buf[0])).AllPrivileges() {
		out = append(out, TokenPrivilege{Name: privilegeName(p.Luid), Attributes: p.Attributes})
	}
	return out, nil
}

func privilegeName(luid windows.LUID) string {
	buf := make([]uint16, 128)
	n := uint32(len(buf))
	r, _, _ := procLookupPrivilegeNameW.Call(0, uintptr(unsafe.Pointer(&luid)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return fmt.Sprintf("luid %08x:%08x", luid.HighPart, luid.LowPart)
	}
	return windows.UTF16ToString(buf[:n])
}

// logonSession reads the logon type and authentication package of a logon
// session, or the NTSTATUS-derived error when LSA refuses the query.
func logonSession(id windows.LUID) (uint32, string, uint32) {
	var data *logonSessionData
	r, _, _ := procLsaGetLogonSessionData.Call(uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&data)))
	if r != 0 {
		return 0, "", uint32(windows.NTStatus(r).Errno())
	}
	if data == nil {
		return 0, "", uint32(windows.ERROR_INVALID_DATA)
	}
	defer procLsaFreeReturnBuffer.Call(uintptr(unsafe.Pointer(data)))
	return data.LogonType, data.AuthenticationPackage.String(), 0
}

func win32Code(err error) uint32 {
	var errno windows.Errno
	if errors.As(err, &errno) {
		return uint32(errno)
	}
	return uint32(windows.ERROR_GEN_FAILURE)
}
