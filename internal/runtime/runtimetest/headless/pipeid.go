package headless

import "fmt"

// Caller decision reasons of the SYSTEM qualification pipe.
const (
	ReasonAccepted              = "accepted"
	ReasonOpen                  = "open"                   // the caller could not be opened and held
	ReasonRequest               = "request"                // no single well-formed request was read
	ReasonImpersonation         = "impersonation"          // no Identification-level token
	ReasonExited                = "exited"                 // the held caller exited before the decision
	ReasonAccount               = "account"                // the caller's account is not the allowed one
	ReasonImpersonationMismatch = "impersonation-mismatch" // thread and process tokens name other accounts
	ReasonClaim                 = "claim"                  // a claimed incarnation does not match the held one
)

// Claim is a client's optional statement of its own incarnation. It is
// untrusted: the server compares it with what it holds.
type Claim struct {
	PID     uint32 `json:"pid"`
	Created uint64 `json:"created"`
	SID     string `json:"sid,omitempty"`
}

// CallerObservation is what the server established about one connection:
// the client PID from Windows, the process it opened and held by that PID,
// that process's creation time and primary-token account, whether it read
// one well-formed request, the account of the Identification-level
// impersonation token of that request, and whether the held process had
// exited by the decision.
type CallerObservation struct {
	PID                uint32 `json:"pid"`
	OpenError          uint32 `json:"openError,omitempty"`
	Created            uint64 `json:"created,omitempty"`
	ProcessSID         string `json:"processSid,omitempty"`
	RequestError       string `json:"requestError,omitempty"`
	ImpersonationError uint32 `json:"impersonationError,omitempty"`
	ImpersonationSID   string `json:"impersonationSid,omitempty"`
	Exited             bool   `json:"exited"`
	Claim              *Claim `json:"claim,omitempty"`
}

// Decide accepts a caller only for the allowed account, on a held, live
// process whose primary token and impersonation token name that account,
// and whose claimed incarnation, when it sends one, is the held one. The
// result identifies an account and a process incarnation, not a unit,
// definition or launch: any process of the account is accepted.
func Decide(allowedSID string, o CallerObservation) (bool, string) {
	switch {
	case o.PID == 0 || o.OpenError != 0 || o.Created == 0 || o.ProcessSID == "":
		return false, ReasonOpen
	case o.RequestError != "":
		return false, ReasonRequest
	case o.ImpersonationError != 0 || o.ImpersonationSID == "":
		return false, ReasonImpersonation
	case o.Exited:
		return false, ReasonExited
	case o.ImpersonationSID != o.ProcessSID:
		return false, ReasonImpersonationMismatch
	case o.ProcessSID != allowedSID:
		return false, ReasonAccount
	case o.Claim != nil && (o.Claim.PID != o.PID || o.Claim.Created != o.Created || o.Claim.SID != "" && o.Claim.SID != o.ProcessSID):
		return false, ReasonClaim
	}
	return true, ReasonAccepted
}

// ServerEntry is one connection as the server saw and decided it.
type ServerEntry struct {
	Observation CallerObservation `json:"observation"`
	Verdict     Verdict           `json:"verdict"`
}

// ServerIdentity is the qualification server's own process and token.
type ServerIdentity struct {
	PID     uint32 `json:"pid"`
	Created uint64 `json:"created"`
	SID     string `json:"sid"`
	Session uint32 `json:"session"`
}

// ServerReport is the pipe server's bounded report: its own identity, the
// pipe, the allowed account, the ACL and every connection's raw
// observation and verdict.
type ServerReport struct {
	Name    string         `json:"name"`
	Allowed string         `json:"allowed"`
	ACL     []string       `json:"acl"`
	Server  ServerIdentity `json:"server"`
	Entries []ServerEntry  `json:"entries"`
}

// Verdict is the server's reply to one connection. Held reports that the
// server held the caller's process through its decision.
type Verdict struct {
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason"`
	Held     bool   `json:"held"`
	PID      uint32 `json:"pid,omitempty"`
	Created  uint64 `json:"created,omitempty"`
}

// PipeResult is one client's sub-result: how its open ended and, when it
// connected, its own incarnation, the server's verdict and whether the
// server held it.
type PipeResult struct {
	Client    string `json:"client"`
	OpenError uint32 `json:"openError,omitempty"`
	Connected bool   `json:"connected"`
	PID       uint32 `json:"pid,omitempty"`
	Created   uint64 `json:"created,omitempty"`
	Accepted  bool   `json:"accepted"`
	Reason    string `json:"reason,omitempty"`
	Held      bool   `json:"held"`
}

// Pipe clients of H15 and H16.
const (
	ClientInUnit          = "in-unit"
	ClientOutsideUnit     = "outside-unit"
	ClientWrongDecision   = "wrong-account-decision"
	ClientWrongACL        = "wrong-account-acl"
	ClientExited          = "exited"
	ClientStaleClaim      = "stale-claim"
	ClientSystemOnly      = "system-only"
	ClientPeerUserPipe    = "peer-user-pipe"
	ClientControlPipe     = "control-pipe"
	ClientMaintenancePipe = "maintenance-pipe"
)

// CheckPipe verifies a pipe sub-result set against what H15 or H16 requires.
func CheckPipe(set string, results []PipeResult) []string {
	accepted := func(r PipeResult) bool { return r.Connected && r.Accepted && r.Reason == ReasonAccepted && r.Held }
	denied := func(reason string) func(PipeResult) bool {
		return func(r PipeResult) bool { return r.Connected && !r.Accepted && r.Reason == reason }
	}
	aclDenied := func(r PipeResult) bool { return !r.Connected && r.OpenError == errAccessDenied }
	var want map[string]func(PipeResult) bool
	switch set {
	case PipeAuthorized:
		want = map[string]func(PipeResult) bool{
			ClientInUnit: accepted, ClientOutsideUnit: accepted,
			ClientWrongDecision: denied(ReasonAccount), ClientWrongACL: aclDenied,
			ClientExited: denied(ReasonExited), ClientStaleClaim: denied(ReasonClaim),
		}
	case PipeDenied:
		want = map[string]func(PipeResult) bool{
			ClientSystemOnly: aclDenied, ClientPeerUserPipe: aclDenied, ClientControlPipe: aclDenied, ClientMaintenancePipe: aclDenied,
		}
	default:
		return []string{"unknown pipe set " + set}
	}
	got := map[string]PipeResult{}
	for _, r := range results {
		if _, dup := got[r.Client]; dup {
			return []string{"repeated client " + r.Client}
		}
		got[r.Client] = r
	}
	var problems []string
	for client, check := range want {
		r, ok := got[client]
		switch {
		case !ok:
			problems = append(problems, client+" missing")
		case !check(r):
			problems = append(problems, fmt.Sprintf("%s: connected=%t accepted=%t reason=%q open error %d", client, r.Connected, r.Accepted, r.Reason, r.OpenError))
		}
	}
	return problems
}
