package headless

import "fmt"

// Characterizations. Succeeded and refused are both qualification
// evidence; inconclusive and failed leave the case open.
const (
	Succeeded    = "succeeded"
	Refused      = "refused"
	Inconclusive = "inconclusive"
	Failed       = "failed"
)

// Win32 and Winsock codes the classifiers name.
const (
	errFileNotFound       = 2
	errPathNotFound       = 3
	errAccessDenied       = 5
	errBadNetPath         = 53
	errBadNetName         = 67
	errLogonFailure       = 1326
	errAccountRestriction = 1327
	errNoSuchLogonSession = 1312
	errLogonTypeNotGrant  = 1385
	errTrustFailure       = 1789
	errDecryptionFailed   = 6000
	errNoUserKeys         = 6006
	wsaETimedOut          = 10060
	wsaEConnRefused       = 10061
)

// OpResult is one file operation's native result.
type OpResult struct {
	Op     string `json:"op"`
	OK     bool   `json:"ok"`
	Win32  uint32 `json:"win32,omitempty"`
	Bytes  int    `json:"bytes,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// TCPResult is one nonce round trip from the workload token.
type TCPResult struct {
	Target    string `json:"target"` // loopback or peer
	Connected bool   `json:"connected"`
	Error     uint32 `json:"error,omitempty"` // Winsock code
	Sent      int    `json:"sent"`
	Received  int    `json:"received"`
	Nonce     string `json:"nonce"`
	Echoed    bool   `json:"echoed"`
}

// EchoReceipt is the echo server's own record of a nonce it received.
type EchoReceipt struct {
	Nonce    string `json:"nonce"`
	Received bool   `json:"received"`
}

// SMBResult is the bare S4U UNC probe: TCP 445 reachability, then a read of
// the expected nonce file and a write, with no supplied credentials.
type SMBResult struct {
	Reachable    bool     `json:"reachable"`
	ReachError   uint32   `json:"reachError,omitempty"`
	Read         OpResult `json:"read"`
	Write        OpResult `json:"write"`
	ExpectSHA256 string   `json:"expectSha256"`
}

// Principal is the server's record of who authenticated: an account role
// (A, B, control) or guest, anonymous or machine access.
type Principal struct {
	Class string `json:"class"` // account, guest, anonymous, machine, none
	Role  string `json:"role,omitempty"`
}

// EFSResult is the bare S4U plaintext read of an encrypted fixture.
type EFSResult struct {
	VolumeEncryption bool     `json:"volumeEncryption"`
	VolumeError      uint32   `json:"volumeError,omitempty"`
	Encrypted        bool     `json:"encrypted"`
	Read             OpResult `json:"read"`
	Plain            OpResult `json:"plain"`
	ExpectSHA256     string   `json:"expectSha256"`
}

// CharacterizeTCP judges H17: loopback must work; the isolated peer counts
// only with the echoed nonce and the server's own receipt.
func CharacterizeTCP(results []TCPResult, receipt *EchoReceipt) (string, string) {
	var loop, peer *TCPResult
	for i := range results {
		switch results[i].Target {
		case "loopback":
			loop = &results[i]
		case "peer":
			peer = &results[i]
		}
	}
	switch {
	case loop == nil || peer == nil:
		return Failed, "a loopback and a peer round trip are both required"
	case !loop.Connected || !loop.Echoed:
		return Failed, fmt.Sprintf("loopback round trip failed (error %d)", loop.Error)
	case !peer.Connected:
		return Inconclusive, fmt.Sprintf("peer unreachable (error %d)", peer.Error)
	case !peer.Echoed:
		return Inconclusive, "peer did not echo the nonce"
	case receipt == nil || !receipt.Received || receipt.Nonce != peer.Nonce:
		return Inconclusive, "the peer did not record receiving the nonce"
	}
	return Succeeded, "nonce round trip on the isolated route, confirmed at both ends"
}

func authError(code uint32) bool {
	switch code {
	case errAccessDenied, errLogonFailure, errAccountRestriction, errNoSuchLogonSession, errLogonTypeNotGrant, errTrustFailure:
		return true
	}
	return false
}

// CharacterizeSMB judges H18. An authentication error counts as a refusal
// only when the share is reachable and a password-bearing control reached
// it; a success counts only when the server names the account itself.
func CharacterizeSMB(r *SMBResult, passwordControl bool, server *Principal, account string) (string, string) {
	switch {
	case r == nil:
		return Failed, "no SMB probe"
	case !r.Reachable:
		return Inconclusive, fmt.Sprintf("TCP 445 unreachable (error %d)", r.ReachError)
	case r.Read.OK && r.Write.OK:
		if r.Read.SHA256 != r.ExpectSHA256 {
			return Failed, "read a different nonce"
		}
		if server == nil || server.Class != "account" || server.Role != account {
			return Inconclusive, "access succeeded but the server did not attribute it to the account"
		}
		return Succeeded, "bare S4U access authenticated as the account"
	}
	code := r.Read.Win32
	if r.Read.OK {
		code = r.Write.Win32
	}
	switch {
	case !authError(code):
		return Inconclusive, fmt.Sprintf("network or share error %d", code)
	case !passwordControl:
		return Inconclusive, "no passing password-bearing control on the same share"
	}
	return Refused, fmt.Sprintf("bare S4U refused with %d while the share works with a password logon", code)
}

func efsError(code uint32) bool {
	switch code {
	case errAccessDenied, errDecryptionFailed, errNoUserKeys:
		return true
	}
	return false
}

// CharacterizeEFS judges H19. A failure counts as a refusal only when the
// volume supports encryption, the fixture is encrypted, an unencrypted
// sibling with the same ACL is readable and a password logon decrypts it.
func CharacterizeEFS(r *EFSResult, passwordControl bool) (string, string) {
	switch {
	case r == nil:
		return Failed, "no EFS probe"
	case !r.VolumeEncryption:
		return Inconclusive, fmt.Sprintf("the volume does not support encryption (error %d)", r.VolumeError)
	case !r.Encrypted:
		return Inconclusive, "the fixture is not encrypted"
	case !r.Plain.OK:
		return Inconclusive, fmt.Sprintf("the plain sibling is unreadable (error %d): an ACL problem, not EFS", r.Plain.Win32)
	case r.Read.OK && r.Read.SHA256 != r.ExpectSHA256:
		return Failed, "read different plaintext"
	case r.Read.OK:
		return Succeeded, "plaintext read under bare S4U for this profile and key state"
	case !efsError(r.Read.Win32):
		return Inconclusive, fmt.Sprintf("unexpected read error %d", r.Read.Win32)
	case !passwordControl:
		return Inconclusive, "no passing password-logon decryption control"
	}
	return Refused, fmt.Sprintf("bare S4U read refused with %d while a password logon decrypts", r.Read.Win32)
}

// PathResult is one path sub-probe of H13 or H14.
type PathResult struct {
	Probe string `json:"probe"`
	OK    bool   `json:"ok"`
	Win32 uint32 `json:"win32,omitempty"`
}

// CheckPaths verifies a path set's sub-results by their native codes.
func CheckPaths(set string, results []PathResult) []string {
	got := map[string]PathResult{}
	for _, r := range results {
		got[r.Probe] = r
	}
	want := map[string]func(PathResult) bool{}
	ok := func(r PathResult) bool { return r.OK }
	switch set {
	case PathsOwnRoots:
		want = map[string]func(PathResult) bool{
			"unit-fixture": ok, "state-write": ok, "state-read": ok, "hkcu": ok, "known-folders": ok,
			"peer-root": func(r PathResult) bool { return !r.OK && r.Win32 == errAccessDenied },
		}
	case PathsMissingAndDenied:
		want = map[string]func(PathResult) bool{
			"absent": func(r PathResult) bool { return !r.OK && (r.Win32 == errFileNotFound || r.Win32 == errPathNotFound) },
			"denied": func(r PathResult) bool { return !r.OK && r.Win32 == errAccessDenied },
		}
	}
	var problems []string
	for name, check := range want {
		r, found := got[name]
		switch {
		case !found:
			problems = append(problems, name+" missing")
		case !check(r):
			problems = append(problems, fmt.Sprintf("%s result ok=%t error %d", name, r.OK, r.Win32))
		}
	}
	return problems
}
