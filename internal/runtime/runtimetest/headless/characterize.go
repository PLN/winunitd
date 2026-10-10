package headless

import (
	"fmt"
	"strings"
)

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
// the expected nonce file and a write, with no supplied credentials. Target
// is the nonce file's UNC path; Started and Ended bound the file calls, as
// FILETIMEs.
type SMBResult struct {
	Target     string   `json:"target"`
	Started    uint64   `json:"started"`
	Ended      uint64   `json:"ended"`
	Reachable  bool     `json:"reachable"`
	ReachError uint32   `json:"reachError,omitempty"`
	Read       OpResult `json:"read"`
	Write      OpResult `json:"write"`
	// WriteTarget is the new file the probe tried to create beside Target.
	WriteTarget  string `json:"writeTarget,omitempty"`
	ExpectSHA256 string `json:"expectSha256"`
}

// Principal is the server's record of an access to Target at At (a
// FILETIME): an account, by SID, or guest, anonymous or machine access.
type Principal struct {
	Class  string `json:"class"` // account, guest, anonymous, machine, none
	SID    string `json:"sid,omitempty"`
	Target string `json:"target,omitempty"`
	At     uint64 `json:"at,omitempty"`
}

// EFSResult is the bare S4U plaintext read of the encrypted fixture at
// Target.
type EFSResult struct {
	Target           string   `json:"target"`
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

// attributionSlack is how far a server's attribution may lie outside the
// probe's own interval, in 100 ns units.
const attributionSlack = 5 * 1e7

// CharacterizeSMB judges H18 for the account sid. An authentication error
// counts as a refusal only when the share is reachable and the same
// account's password-bearing logon read the same nonce file; a success
// counts only when the server attributes that access, on that file and at
// that time, to the account itself.
func CharacterizeSMB(r *SMBResult, password *SMBResult, server *Principal, sid string) (string, string) {
	switch {
	case r == nil:
		return Failed, "no SMB probe"
	case !r.Reachable:
		return Inconclusive, fmt.Sprintf("TCP 445 unreachable (error %d)", r.ReachError)
	case r.Read.OK && r.Write.OK:
		if r.Read.SHA256 != r.ExpectSHA256 {
			return Failed, "read a different nonce"
		}
		switch {
		case server == nil || server.Class != "account":
			return Inconclusive, "access succeeded but the server did not attribute it to an account"
		case server.SID != sid:
			return Inconclusive, "access succeeded but the server attributed it to another account"
		case server.Target != r.Target || server.At+attributionSlack < r.Started || server.At > r.Ended+attributionSlack:
			return Inconclusive, "the server's attribution is not for this access"
		}
		return Succeeded, "bare S4U access authenticated as the account"
	}
	// The operation that failed is the discriminator: the same account's
	// password-bearing logon must succeed on that operation, at that place.
	// A write both contexts are denied is a share permission, not a
	// credential refusal.
	code, op := r.Read.Win32, "read"
	if r.Read.OK {
		code, op = r.Write.Win32, "write"
	}
	switch {
	case !authError(code):
		return Inconclusive, fmt.Sprintf("network or share error %d", code)
	case password == nil || !password.Reachable:
		return Inconclusive, "no passing same-account password-bearing control"
	case password.Target != r.Target:
		return Inconclusive, "the password-bearing control used another file"
	case op == "read" && (!password.Read.OK || password.Read.SHA256 != r.ExpectSHA256):
		return Inconclusive, "the same account's password logon did not read the file either"
	case op == "write" && (!password.Write.OK || r.WriteTarget == "" || uncDir(password.WriteTarget) != uncDir(r.WriteTarget)):
		return Inconclusive, "the same account's password logon did not write beside the file either: a share permission, not a credential refusal"
	}
	return Refused, fmt.Sprintf("bare S4U %s refused with %d where the same account's password logon succeeds", op, code)
}

// uncDir is the directory of a Windows path, on any system.
func uncDir(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return strings.ToLower(p[:i])
	}
	return ""
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
// sibling with the same ACL is readable and the same account's password
// logon decrypts the same file to the expected plaintext.
func CharacterizeEFS(r *EFSResult, password *EFSResult) (string, string) {
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
	case password == nil || !password.Read.OK:
		return Inconclusive, "no passing password-logon decryption control"
	case password.Target != r.Target || password.Read.SHA256 != r.ExpectSHA256:
		return Inconclusive, "the password logon decrypted another file"
	}
	return Refused, fmt.Sprintf("bare S4U read refused with %d while the same account's password logon decrypts it", r.Read.Win32)
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
	var want map[string]func(PathResult) bool
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
	case PathsDaemonDenial:
		denied := func(r PathResult) bool { return !r.OK && r.Win32 == errAccessDenied }
		want = map[string]func(PathResult) bool{
			"own-log": ok, "peer-directory": denied, "peer-log": denied, "system-directory": denied, "system-log": denied,
		}
	default:
		return []string{"unknown path set " + set}
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
