//go:build windows

package runtime

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	kerbS4ULogon     = 12
	msv1_0S4ULogon   = 12
	logonTypeNetwork = 3
	logonTypeBatch   = 4
)

type unicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

type lsaString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *byte
}

type kerbS4ULogonInfo struct {
	MessageType uint32
	Flags       uint32
	ClientUpn   unicodeString
	ClientRealm unicodeString
}

type msvS4ULogonInfo struct {
	MessageType uint32
	Flags       uint32
	UPN         unicodeString
	DomainName  unicodeString
}

type tokenSource struct {
	SourceName       [8]byte
	SourceIdentifier windows.LUID
}

type quotaLimits struct {
	PagedPoolLimit        uintptr
	NonPagedPoolLimit     uintptr
	MinimumWorkingSetSize uintptr
	MaximumWorkingSetSize uintptr
	PagefileLimit         uintptr
	TimeLimit             int64
}

var (
	modSecur32                     = windows.NewLazySystemDLL("secur32.dll")
	modAdvapi32                    = windows.NewLazySystemDLL("advapi32.dll")
	procLsaConnectUntrusted        = modSecur32.NewProc("LsaConnectUntrusted")
	procLsaLookupAuthenticationPkg = modSecur32.NewProc("LsaLookupAuthenticationPackage")
	procLsaLogonUser               = modSecur32.NewProc("LsaLogonUser")
	procLsaDeregisterLogonProcess  = modSecur32.NewProc("LsaDeregisterLogonProcess")
	procLsaFreeReturnBuffer        = modSecur32.NewProc("LsaFreeReturnBuffer")
	procLsaNtStatusToWinError      = modAdvapi32.NewProc("LsaNtStatusToWinError")
	procCredReadW                  = modAdvapi32.NewProc("CredReadW")
	procCredFree                   = modAdvapi32.NewProc("CredFree")
	procLogonUserW                 = modAdvapi32.NewProc("LogonUserW")
)

const (
	credTypeGeneric        = 1
	logon32LogonNetwork    = 3
	logon32ProviderDefault = 0
)

type credW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

// ObtainLingerToken tries S4U first. If the S4U token has no network
// credentials and rec.CredentialURI is a named CredMan/LSA reference,
// that URI is used only to obtain network creds. Passwords are never
// read from unit files, linger records, environment, or path references.
func ObtainLingerToken(rec LingerRecord) (*UserToken, error) {
	if !validAccountSID(rec.SID) && strings.TrimSpace(rec.Name) == "" {
		return nil, failLinger(rec.SID, fmt.Errorf("SID or account name required"))
	}
	tok, err := s4uLogon(rec)
	if err != nil {
		return nil, failLinger(rec.SID, err)
	}
	if !s4uTokenHasNetworkCreds(tok) && rec.CredentialURI != "" {
		netTok, netErr := tokenFromCredentialURI(rec.CredentialURI)
		if netErr == nil && netTok != nil {
			_ = tok.Close()
			return netTok, nil
		}
		// Keep the S4U token; the named URI is best-effort for network creds.
	}
	return tok, nil
}

func s4uTokenHasNetworkCreds(_ *UserToken) bool {
	// S4U2Self yields a local identity token without network credentials
	// unless constrained delegation is configured. P2 does not implement
	// S4U2Proxy; a named CredMan/LSA URI is the only network fallback.
	return false
}

func s4uLogon(rec LingerRecord) (*UserToken, error) {
	upn, realm := s4uNames(rec)
	if upn == "" {
		return nil, fmt.Errorf("no account name for S4U")
	}

	var lsaHandle windows.Handle
	st, _, _ := procLsaConnectUntrusted.Call(uintptr(unsafe.Pointer(&lsaHandle)))
	if err := lsaStatus(st); err != nil {
		return nil, fmt.Errorf("LsaConnectUntrusted: %w", err)
	}
	defer procLsaDeregisterLogonProcess.Call(uintptr(lsaHandle))

	type attempt struct {
		pkg      string
		build    func() ([]byte, error)
		logonTyp uint32
	}
	try := []attempt{
		{"Kerberos", func() ([]byte, error) { return buildKerbS4U(upn, realm) }, logonTypeNetwork},
		{"MICROSOFT_AUTHENTICATION_PACKAGE_V1_0", func() ([]byte, error) { return buildMSVS4U(upn, realm) }, logonTypeNetwork},
		{"Negotiate", func() ([]byte, error) { return buildKerbS4U(upn, realm) }, logonTypeBatch},
	}

	originBuf := append([]byte("winunitd"), 0)
	origin := lsaString{Length: uint16(len("winunitd")), MaximumLength: uint16(len(originBuf)), Buffer: &originBuf[0]}
	var last error
	for _, t := range try {
		pkgID, err := lsaLookupPackage(lsaHandle, t.pkg)
		if err != nil {
			last = err
			continue
		}
		auth, err := t.build()
		if err != nil {
			last = err
			continue
		}
		var source tokenSource
		copy(source.SourceName[:], []byte("winunitd"))
		var profile uintptr
		var profileLen uint32
		var logonID windows.LUID
		var token windows.Handle
		var quotas quotaLimits
		var subStatus uintptr
		st, _, _ = procLsaLogonUser.Call(
			uintptr(lsaHandle),
			uintptr(unsafe.Pointer(&origin)),
			uintptr(t.logonTyp),
			uintptr(pkgID),
			uintptr(unsafe.Pointer(&auth[0])),
			uintptr(len(auth)),
			0,
			uintptr(unsafe.Pointer(&source)),
			uintptr(unsafe.Pointer(&profile)),
			uintptr(unsafe.Pointer(&profileLen)),
			uintptr(unsafe.Pointer(&logonID)),
			uintptr(unsafe.Pointer(&token)),
			uintptr(unsafe.Pointer(&quotas)),
			uintptr(unsafe.Pointer(&subStatus)),
		)
		if profile != 0 {
			procLsaFreeReturnBuffer.Call(profile)
		}
		if err := lsaStatus(st); err != nil {
			last = fmt.Errorf("%s S4U: %w", t.pkg, err)
			continue
		}
		primary, err := duplicatePrimary(windows.Token(token))
		_ = windows.CloseHandle(token)
		if err != nil {
			last = err
			continue
		}
		info, err := userInfoFromToken(primary)
		if err != nil {
			info, err = fillInfo(rec)
			if err != nil {
				_ = primary.Close()
				last = err
				continue
			}
		}
		if rec.SID != "" && info.SID != "" && !strings.EqualFold(info.SID, rec.SID) {
			_ = primary.Close()
			last = fmt.Errorf("S4U SID %s does not match linger record %s", info.SID, rec.SID)
			continue
		}
		return &UserToken{Info: info, native: winToken(primary)}, nil
	}
	if last == nil {
		last = fmt.Errorf("S4U logon failed")
	}
	return nil, last
}

func s4uNames(rec LingerRecord) (upn, realm string) {
	name := strings.TrimSpace(rec.Name)
	if name == "" && rec.SID != "" {
		info, err := LookupAccountName(rec.SID)
		if err == nil {
			name = FormatAccount(info)
		}
	}
	if name == "" {
		return "", ""
	}
	if i := strings.LastIndex(name, `\`); i >= 0 {
		return name[i+1:], name[:i]
	}
	return name, ""
}

func fillInfo(rec LingerRecord) (UserInfo, error) {
	if rec.SID != "" {
		info, err := LookupAccountName(rec.SID)
		if err == nil {
			applyAccountName(&info, rec.Name)
			return info, nil
		}
	}
	if rec.Name != "" {
		return LookupAccountName(rec.Name)
	}
	return UserInfo{}, fmt.Errorf("cannot resolve linger identity")
}

func applyAccountName(info *UserInfo, name string) {
	if info == nil || strings.TrimSpace(name) == "" || info.Username != "" {
		return
	}
	if i := strings.LastIndex(name, `\`); i >= 0 {
		info.Domain = name[:i]
		info.Username = name[i+1:]
		return
	}
	info.Username = name
}

func buildKerbS4U(upn, realm string) ([]byte, error) {
	upnUTF, err := windows.UTF16FromString(upn)
	if err != nil {
		return nil, err
	}
	realmUTF, err := windows.UTF16FromString(realm)
	if err != nil {
		return nil, err
	}
	hdr := int(unsafe.Sizeof(kerbS4ULogonInfo{}))
	upnBytes := len(upnUTF) * 2
	realmBytes := len(realmUTF) * 2
	buf := make([]byte, hdr+upnBytes+realmBytes)
	info := (*kerbS4ULogonInfo)(unsafe.Pointer(&buf[0]))
	info.MessageType = kerbS4ULogon
	if upnBytes > 0 {
		copy(buf[hdr:], utf16Bytes(upnUTF))
		info.ClientUpn = unicodeString{
			Length:        uint16(upnBytes - 2),
			MaximumLength: uint16(upnBytes),
			Buffer:        (*uint16)(unsafe.Pointer(&buf[hdr])),
		}
	}
	if realmBytes > 0 {
		off := hdr + upnBytes
		copy(buf[off:], utf16Bytes(realmUTF))
		info.ClientRealm = unicodeString{
			Length:        uint16(realmBytes - 2),
			MaximumLength: uint16(realmBytes),
			Buffer:        (*uint16)(unsafe.Pointer(&buf[off])),
		}
	}
	return buf, nil
}

func buildMSVS4U(upn, realm string) ([]byte, error) {
	upnUTF, err := windows.UTF16FromString(upn)
	if err != nil {
		return nil, err
	}
	realmUTF, err := windows.UTF16FromString(realm)
	if err != nil {
		return nil, err
	}
	hdr := int(unsafe.Sizeof(msvS4ULogonInfo{}))
	upnBytes := len(upnUTF) * 2
	realmBytes := len(realmUTF) * 2
	buf := make([]byte, hdr+upnBytes+realmBytes)
	info := (*msvS4ULogonInfo)(unsafe.Pointer(&buf[0]))
	info.MessageType = msv1_0S4ULogon
	if upnBytes > 0 {
		copy(buf[hdr:], utf16Bytes(upnUTF))
		info.UPN = unicodeString{
			Length:        uint16(upnBytes - 2),
			MaximumLength: uint16(upnBytes),
			Buffer:        (*uint16)(unsafe.Pointer(&buf[hdr])),
		}
	}
	if realmBytes > 0 {
		off := hdr + upnBytes
		copy(buf[off:], utf16Bytes(realmUTF))
		info.DomainName = unicodeString{
			Length:        uint16(realmBytes - 2),
			MaximumLength: uint16(realmBytes),
			Buffer:        (*uint16)(unsafe.Pointer(&buf[off])),
		}
	}
	return buf, nil
}

func utf16Bytes(s []uint16) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*2)
}

func lsaLookupPackage(h windows.Handle, name string) (uint32, error) {
	b := append([]byte(name), 0)
	ls := lsaString{Length: uint16(len(name)), MaximumLength: uint16(len(b)), Buffer: &b[0]}
	var id uint32
	st, _, _ := procLsaLookupAuthenticationPkg.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&ls)),
		uintptr(unsafe.Pointer(&id)),
	)
	if err := lsaStatus(st); err != nil {
		return 0, fmt.Errorf("LsaLookupAuthenticationPackage %s: %w", name, err)
	}
	return id, nil
}

func lsaStatus(nt uintptr) error {
	if nt == 0 {
		return nil
	}
	r, _, _ := procLsaNtStatusToWinError.Call(nt)
	if r == 0 {
		return fmt.Errorf("NTSTATUS 0x%x", nt)
	}
	return windows.Errno(r)
}

func duplicatePrimary(tok windows.Token) (windows.Token, error) {
	var primary windows.Token
	err := windows.DuplicateTokenEx(
		tok,
		windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ADJUST_DEFAULT|windows.TOKEN_ADJUST_SESSIONID,
		nil,
		windows.SecurityImpersonation,
		windows.TokenPrimary,
		&primary,
	)
	if err != nil {
		return 0, fmt.Errorf("DuplicateTokenEx: %w", err)
	}
	return primary, nil
}

func tokenFromCredentialURI(raw string) (*UserToken, error) {
	scheme, name, err := credentialURIParts(raw)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("empty credential URI")
	}
	switch scheme {
	case credManScheme:
		return tokenFromCredMan(name)
	case lsaScheme:
		return tokenFromLSASecret(name)
	default:
		return nil, fmt.Errorf("unsupported credential URI scheme")
	}
}

func tokenFromCredMan(target string) (*UserToken, error) {
	targetp, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return nil, err
	}
	var cred *credW
	r1, _, e1 := procCredReadW.Call(uintptr(unsafe.Pointer(targetp)), uintptr(credTypeGeneric), 0, uintptr(unsafe.Pointer(&cred)))
	if r1 == 0 {
		if e1 != syscall.Errno(0) {
			return nil, fmt.Errorf("CredRead: %w", e1)
		}
		return nil, fmt.Errorf("CredRead %q failed", target)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(cred)))
	var blob []byte
	if cred.CredentialBlob != nil && cred.CredentialBlobSize > 0 {
		blob = unsafe.Slice(cred.CredentialBlob, int(cred.CredentialBlobSize))
	}
	defer zeroBytes(blob)
	user := ""
	if cred.UserName != nil {
		user = windows.UTF16PtrToString(cred.UserName)
	}
	u, domain := splitUserDomain(user)
	pass := blobPassword(blob)
	defer zeroString(&pass)
	if u == "" || pass == "" {
		return nil, fmt.Errorf("CredMan credential %q is incomplete", target)
	}
	return logonWithSecret(u, domain, pass)
}

func tokenFromLSASecret(name string) (*UserToken, error) {
	return nil, fmt.Errorf("LSA secret %q is not available", name)
}

func logonWithSecret(user, domain, pass string) (*UserToken, error) {
	userp, err := windows.UTF16PtrFromString(user)
	if err != nil {
		return nil, err
	}
	domainp, err := windows.UTF16PtrFromString(domain)
	if err != nil {
		return nil, err
	}
	passp, err := windows.UTF16PtrFromString(pass)
	if err != nil {
		return nil, err
	}
	var tok windows.Handle
	r1, _, e1 := procLogonUserW.Call(
		uintptr(unsafe.Pointer(userp)),
		uintptr(unsafe.Pointer(domainp)),
		uintptr(unsafe.Pointer(passp)),
		uintptr(logon32LogonNetwork),
		uintptr(logon32ProviderDefault),
		uintptr(unsafe.Pointer(&tok)),
	)
	if r1 == 0 {
		if e1 != syscall.Errno(0) {
			return nil, fmt.Errorf("named credential logon: %w", e1)
		}
		return nil, fmt.Errorf("named credential logon failed")
	}
	primary, err := duplicatePrimary(windows.Token(tok))
	_ = windows.CloseHandle(tok)
	if err != nil {
		return nil, err
	}
	info, err := userInfoFromToken(primary)
	if err != nil {
		_ = primary.Close()
		return nil, err
	}
	return &UserToken{Info: info, native: winToken(primary)}, nil
}

func splitUserDomain(name string) (user, domain string) {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, `\`); i >= 0 {
		return name[i+1:], name[:i]
	}
	if i := strings.Index(name, "@"); i >= 0 {
		return name[:i], name[i+1:]
	}
	return name, ""
}

func blobPassword(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if len(b)%2 == 0 {
		u := unsafe.Slice((*uint16)(unsafe.Pointer(&b[0])), len(b)/2)
		if len(u) > 0 && u[len(u)-1] == 0 {
			u = u[:len(u)-1]
		}
		return windows.UTF16ToString(u)
	}
	return string(b)
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func zeroString(s *string) {
	if s == nil {
		return
	}
	*s = strings.Repeat("\x00", len(*s))
	*s = ""
}
