package runtime

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
)

// Linger token path names logged when ObtainLingerToken succeeds.
const (
	LingerTokenPathS4U      = "s4u"
	LingerTokenPathStoreURI = "store-uri"
)

// LOGON32 / LsaLogonUser logon types (winnt.h). The CredMan/LSA URI
// fallback uses Batch so the token can carry outbound network creds.
// Network (3) does not cache credentials for outbound SSO.
const (
	logonTypeInteractive       = 2
	logonTypeNetwork           = 3
	logonTypeBatch             = 4
	logonTypeService           = 5
	logonTypeUnlock            = 7
	logonTypeNetworkCleartext  = 8
	logonTypeNewCredentials    = 9
	logonTypeRemoteInteractive = 10
	logonTypeCachedInteractive = 11

	logon32LogonBatch      = logonTypeBatch
	logon32LogonNetwork    = logonTypeNetwork // documented only; URI fallback must not use this
	logon32ProviderDefault = 0
)

// lingerLogf, if set, receives path-selection messages (S4U vs store URI).
// The daemon wires this to stderr. Default is a no-op.
var (
	lingerLogMu sync.Mutex
	lingerLogf  func(string, ...any)
)

// SetLingerLogf sets the optional linger path logger.
func SetLingerLogf(fn func(string, ...any)) {
	lingerLogMu.Lock()
	lingerLogf = fn
	lingerLogMu.Unlock()
}

func logLinger(format string, args ...any) {
	lingerLogMu.Lock()
	fn := lingerLogf
	lingerLogMu.Unlock()
	if fn != nil {
		fn(format, args...)
	}
}

// useStoreURIFallback is the path-selection rule: a named CredMan/LSA
// URI is tried only when it is present AND S4U is insufficient for
// outbound network credentials. The Windows probe inspects the logon
// session (LsaGetLogonSessionData LogonType); it is not an always-false
// stub. S4U2Self often yields a Network logon without cached creds
// unless constrained delegation is configured — we do not hardcode that.
func useStoreURIFallback(uri string, s4uHasNetworkCreds bool) bool {
	return strings.TrimSpace(uri) != "" && !s4uHasNetworkCreds
}

// logonTypeCachesOutboundCreds reports whether a Winlogon logon type
// caches credentials that can be presented outbound. Network (3) does
// not. Batch / Interactive / Service / NetworkCleartext / NewCredentials
// and the interactive variants do.
func logonTypeCachesOutboundCreds(logonType uint32) bool {
	switch logonType {
	case logonTypeInteractive, logonTypeBatch, logonTypeService,
		logonTypeUnlock, logonTypeNetworkCleartext, logonTypeNewCredentials,
		logonTypeRemoteInteractive, logonTypeCachedInteractive:
		return true
	default:
		return false
	}
}

// blobPasswordUTF16 decodes a CredMan generic/domain blob as UTF-16LE.
// cmdkey and PowerShell write GENERIC blobs as UTF-16LE; we do not
// guess UTF-8 from even length (that garbles even-length UTF-8).
func blobPasswordUTF16(b []byte) ([]uint16, error) {
	if len(b) == 0 {
		return nil, nil
	}
	if len(b)%2 != 0 {
		return nil, fmt.Errorf("credential blob is not UTF-16LE")
	}
	n := len(b) / 2
	out := make([]uint16, n)
	for i := 0; i < n; i++ {
		out[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	if n > 0 && out[n-1] == 0 {
		out = out[:n-1]
	}
	return out, nil
}

func zeroUTF16(u []uint16) {
	for i := range u {
		u[i] = 0
	}
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
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
