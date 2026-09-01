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
	kerbS4ULogon   = 12
	msv1_0S4ULogon = 12

	credTypeGeneric        = 1
	credTypeDomainPassword = 2

	tokenStatisticsClass = 10 // TokenStatistics
	localSystemSID       = "S-1-5-18"
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

type tokenStatistics struct {
	TokenId            windows.LUID
	AuthenticationId   windows.LUID
	ExpirationTime     int64
	TokenType          uint32
	ImpersonationLevel uint32
	DynamicCharged     uint32
	DynamicAvailable   uint32
	GroupCount         uint32
	PrivilegeCount     uint32
	ModifiedId         windows.LUID
}

// securityLogonSessionData is the leading fields of
// SECURITY_LOGON_SESSION_DATA on amd64 (MSVC layout).
type securityLogonSessionData struct {
	Size                  uint32
	LogonId               windows.LUID
	_                     uint32
	UserName              unicodeString
	LogonDomain           unicodeString
	AuthenticationPackage unicodeString
	LogonType             uint32
	Session               uint32
	Sid                   uintptr
	LogonTime             int64
}

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

var (
	modSecur32                     = windows.NewLazySystemDLL("secur32.dll")
	modAdvapi32                    = windows.NewLazySystemDLL("advapi32.dll")
	modKernel32                    = windows.NewLazySystemDLL("kernel32.dll")
	procLsaRegisterLogonProcess    = modSecur32.NewProc("LsaRegisterLogonProcess")
	procLsaLookupAuthenticationPkg = modSecur32.NewProc("LsaLookupAuthenticationPackage")
	procLsaLogonUser               = modSecur32.NewProc("LsaLogonUser")
	procLsaDeregisterLogonProcess  = modSecur32.NewProc("LsaDeregisterLogonProcess")
	procLsaFreeReturnBuffer        = modSecur32.NewProc("LsaFreeReturnBuffer")
	procLsaGetLogonSessionData     = modSecur32.NewProc("LsaGetLogonSessionData")
	procLsaNtStatusToWinError      = modAdvapi32.NewProc("LsaNtStatusToWinError")
	procCredReadW                  = modAdvapi32.NewProc("CredReadW")
	procCredFree                   = modAdvapi32.NewProc("CredFree")
	procLogonUserW                 = modAdvapi32.NewProc("LogonUserW")
	procAllocateLocallyUniqueId    = modKernel32.NewProc("AllocateLocallyUniqueId")
)

// ObtainLingerToken tries S4U first over a trusted LSA connection
// (LsaRegisterLogonProcess: LocalSystem has SeTcbPrivilege). An
// untrusted connection yields an identification-level token that
// duplicatePrimary rejects.
//
// A named CredMan/LSA URI on the linger record is used only when it is
// present AND the S4U token is insufficient for outbound network
// credentials (real logon-session probe). The URI fallback calls
// LogonUserW with LOGON32_LOGON_BATCH so the token can carry outbound
// network creds. Passwords are never read from unit files, linger
// records, environment, or path references.
func ObtainLingerToken(rec LingerRecord) (*UserToken, error) {
	if !validAccountSID(rec.SID) && strings.TrimSpace(rec.Name) == "" {
		return nil, failLinger(rec.SID, fmt.Errorf("SID or account name required"))
	}
	tok, err := s4uLogon(rec)
	if err != nil {
		return nil, failLinger(rec.SID, err)
	}
	tok.Source = LingerTokenPathS4U
	if useStoreURIFallback(rec.CredentialURI, tokenHasOutboundNetworkCreds(tok)) {
		netTok, netErr := tokenFromCredentialURI(rec.CredentialURI)
		if netErr == nil && netTok != nil {
			_ = tok.Close()
			netTok.Source = LingerTokenPathStoreURI
			logLinger("linger token %s via store-uri (S4U insufficient for outbound network creds)", rec.SID)
			return netTok, nil
		}
		logLinger("linger token %s via s4u (store URI failed: %v)", rec.SID, netErr)
		return tok, nil
	}
	logLinger("linger token %s via s4u", rec.SID)
	return tok, nil
}

func s4uLogon(rec LingerRecord) (*UserToken, error) {
	upn, realm := s4uNames(rec)
	if upn == "" {
		return nil, fmt.Errorf("no account name for S4U")
	}

	lsaHandle, err := connectTrustedLSA()
	if err != nil {
		return nil, err
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
		source := newTokenSource()
		var profile uintptr
		var profileLen uint32
		var logonID windows.LUID
		var token windows.Handle
		var quotas quotaLimits
		var subStatus uintptr
		st, _, _ := procLsaLogonUser.Call(
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

// connectTrustedLSA registers as a logon process. LocalSystem has
// SeTcbPrivilege; LsaConnectUntrusted is not used (identification-level
// tokens fail duplicatePrimary with ERROR_BAD_IMPERSONATION_LEVEL).
func connectTrustedLSA() (windows.Handle, error) {
	if err := enableSeTcbPrivilege(); err != nil {
		// Privilege may already be enabled; LsaRegisterLogonProcess
		// is the authority on whether we are trusted.
		_ = err
	}
	name := "winunitd"
	nameBuf := append([]byte(name), 0)
	ls := lsaString{Length: uint16(len(name)), MaximumLength: uint16(len(nameBuf)), Buffer: &nameBuf[0]}
	var handle windows.Handle
	var mode uint32
	st, _, _ := procLsaRegisterLogonProcess.Call(
		uintptr(unsafe.Pointer(&ls)),
		uintptr(unsafe.Pointer(&handle)),
		uintptr(unsafe.Pointer(&mode)),
	)
	if err := lsaStatus(st); err != nil {
		return 0, fmt.Errorf("LsaRegisterLogonProcess: %w", err)
	}
	return handle, nil
}

func enableSeTcbPrivilege() error {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return err
	}
	defer tok.Close()
	var luid windows.LUID
	name, err := windows.UTF16PtrFromString("SeTcbPrivilege")
	if err != nil {
		return err
	}
	if err := windows.LookupPrivilegeValue(nil, name, &luid); err != nil {
		return err
	}
	tp := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges: [1]windows.LUIDAndAttributes{{
			Luid:       luid,
			Attributes: windows.SE_PRIVILEGE_ENABLED,
		}},
	}
	return windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil)
}

func newTokenSource() tokenSource {
	var src tokenSource
	copy(src.SourceName[:], []byte("winunitd"))
	_, _, _ = procAllocateLocallyUniqueId.Call(uintptr(unsafe.Pointer(&src.SourceIdentifier)))
	return src
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

// tokenHasOutboundNetworkCreds inspects the token's logon session.
// Network / unknown logon types do not cache outbound credentials.
// Probe failure is treated as insufficient (try the URI if present).
func tokenHasOutboundNetworkCreds(tok *UserToken) bool {
	native, ok := nativeToken(tok)
	if !ok {
		return false
	}
	var stats tokenStatistics
	var ret uint32
	err := windows.GetTokenInformation(
		native,
		tokenStatisticsClass,
		(*byte)(unsafe.Pointer(&stats)),
		uint32(unsafe.Sizeof(stats)),
		&ret,
	)
	if err != nil {
		return false
	}
	var data *securityLogonSessionData
	st, _, _ := procLsaGetLogonSessionData.Call(
		uintptr(unsafe.Pointer(&stats.AuthenticationId)),
		uintptr(unsafe.Pointer(&data)),
	)
	if err := lsaStatus(st); err != nil || data == nil {
		return false
	}
	defer procLsaFreeReturnBuffer.Call(uintptr(unsafe.Pointer(data)))
	return logonTypeCachesOutboundCreds(data.LogonType)
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
	var last error
	for _, credType := range []uint32{credTypeGeneric, credTypeDomainPassword} {
		tok, err := readCredAndLogon(targetp, credType, target)
		if err == nil {
			return tok, nil
		}
		last = err
	}
	return nil, last
}

func readCredAndLogon(targetp *uint16, credType uint32, target string) (*UserToken, error) {
	var cred *credW
	r1, _, e1 := procCredReadW.Call(uintptr(unsafe.Pointer(targetp)), uintptr(credType), 0, uintptr(unsafe.Pointer(&cred)))
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
	pass, err := blobPasswordUTF16(blob)
	if err != nil {
		return nil, fmt.Errorf("CredMan credential %q: %w", target, err)
	}
	defer zeroUTF16(pass)
	if u == "" || len(pass) == 0 {
		return nil, fmt.Errorf("CredMan credential %q is incomplete", target)
	}
	return logonWithSecret(u, domain, pass)
}

func tokenFromLSASecret(name string) (*UserToken, error) {
	return nil, fmt.Errorf("LSA secret %q is not available", name)
}

func logonWithSecret(user, domain string, pass []uint16) (*UserToken, error) {
	userp, err := windows.UTF16PtrFromString(user)
	if err != nil {
		return nil, err
	}
	domainp, err := windows.UTF16PtrFromString(domain)
	if err != nil {
		return nil, err
	}
	// Keep the password as []uint16 through LogonUserW; zero after use.
	// Do not copy through an immutable Go string.
	passBuf := make([]uint16, len(pass)+1)
	copy(passBuf, pass)
	defer zeroUTF16(passBuf)
	var tok windows.Handle
	r1, _, e1 := procLogonUserW.Call(
		uintptr(unsafe.Pointer(userp)),
		uintptr(unsafe.Pointer(domainp)),
		uintptr(unsafe.Pointer(&passBuf[0])),
		uintptr(logon32LogonBatch),
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

func runningAsLocalSystem() bool {
	info, err := CurrentUserInfo()
	if err != nil {
		return false
	}
	return strings.EqualFold(info.SID, localSystemSID)
}
