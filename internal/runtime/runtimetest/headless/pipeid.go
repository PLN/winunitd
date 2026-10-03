package headless

import (
	"fmt"
	"slices"
	"strings"
)

// QualificationPipePrefix is the namespace of the fixture's own pipes. It is
// not a WinUnit endpoint.
const QualificationPipePrefix = `\\.\pipe\winunitd-qual\`

// WorkloadPipe is the workload's command pipe for its account: SYSTEM and
// the account itself may connect.
func WorkloadPipe(sid string) string { return QualificationPipePrefix + `workload\` + sid }

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
// A caller that exits cannot report; the server's own entry proves that
// refusal (CheckPipeClients).
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
			ClientWrongDecision: denied(ReasonAccount), ClientWrongACL: aclDenied, ClientStaleClaim: denied(ReasonClaim),
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

// inUnit reports whether a process is part of the account's unit as the
// observer held it: the workload, or a process started by one, at any
// depth.
func inUnit(rep *ObserverReport, pid uint32, created uint64, account string) bool {
	if rep == nil || pid == 0 {
		return false
	}
	find := func(pid uint32, created uint64) *Generation {
		for i, g := range rep.Generations {
			if g.PID == pid && (created == 0 || g.Created == created) && g.Account == account {
				return &rep.Generations[i]
			}
		}
		return nil
	}
	g := find(pid, created)
	for depth := 0; g != nil && depth < 8; depth++ {
		switch g.Role {
		case RoleWorkload:
			return true
		case RoleChild:
			parent := find(g.ParentPID, 0)
			if parent == nil || parent.Created > g.Created {
				return false
			}
			g = parent
		default:
			return false
		}
	}
	return false
}

// CheckPipeClients binds the clients of H15 and H16 to the observer and,
// for H15, recomputes every decision from the SYSTEM server's own
// observations: the in-unit clients are processes of the account's unit;
// the outside-unit client is the account but not its unit; the wrong-account
// client is the peer through a server ACL that admitted it, and is denied at
// a server ACL that does not; an exited caller and a stale claim are refused
// for those reasons.
func CheckPipeClients(set string, results []PipeResult, servers []ServerReport, sid, peerSID, account string, rep *ObserverReport) []string {
	by := map[string]PipeResult{}
	for _, r := range results {
		by[r.Client] = r
	}
	var problems []string
	if set == PipeDenied {
		for _, client := range []string{ClientSystemOnly, ClientPeerUserPipe, ClientControlPipe, ClientMaintenancePipe} {
			if r := by[client]; !inUnit(rep, r.PID, r.Created, account) {
				problems = append(problems, client+" was not a process of the account's unit")
			}
		}
		return problems
	}
	if len(servers) == 0 {
		return []string{"no SYSTEM qualification server report"}
	}
	type found struct {
		server *ServerReport
		entry  *ServerEntry
	}
	var entries []found
	for i := range servers {
		s := &servers[i]
		if s.Server.SID != SystemSID || s.Server.Session != 0 || s.Server.PID == 0 || s.Server.Created == 0 {
			problems = append(problems, "a qualification server was not SYSTEM in session zero")
		}
		if !strings.HasPrefix(s.Name, QualificationPipePrefix) || s.Allowed != sid {
			problems = append(problems, "a qualification server allowed another account")
		}
		for j := range s.Entries {
			en := &s.Entries[j]
			if ok, reason := Decide(s.Allowed, en.Observation); ok != en.Verdict.Accepted || reason != en.Verdict.Reason {
				problems = append(problems, "a server verdict does not follow from its observation")
			}
			entries = append(entries, found{s, en})
		}
	}
	of := func(r PipeResult) *found {
		for i := range entries {
			if o := entries[i].entry.Observation; r.PID != 0 && o.PID == r.PID && o.Created == r.Created {
				return &entries[i]
			}
		}
		return nil
	}
	decided := func(client, reason, processSID string) *found {
		r, ok := by[client]
		f := of(r)
		switch {
		case !ok || f == nil:
			problems = append(problems, client+" has no server entry")
			return nil
		case f.entry.Verdict.Reason != reason || f.entry.Observation.ProcessSID != processSID:
			problems = append(problems, client+" was not decided "+reason+" for its account")
			return nil
		case r.Accepted != f.entry.Verdict.Accepted || r.Reason != f.entry.Verdict.Reason:
			problems = append(problems, client+" received another verdict than the server recorded")
		}
		return f
	}
	if decided(ClientInUnit, ReasonAccepted, sid) != nil && !inUnit(rep, by[ClientInUnit].PID, by[ClientInUnit].Created, account) {
		problems = append(problems, "the in-unit client was not a process of the account's unit")
	}
	if decided(ClientOutsideUnit, ReasonAccepted, sid) != nil && inUnit(rep, by[ClientOutsideUnit].PID, by[ClientOutsideUnit].Created, account) {
		problems = append(problems, "the outside-unit client was a process of the account's unit")
	}
	if f := decided(ClientWrongDecision, ReasonAccount, peerSID); f != nil && !slices.Contains(f.server.ACL, peerSID) {
		problems = append(problems, "the wrong-account decision was not made through an ACL that admitted it")
	}
	if !slices.ContainsFunc(servers, func(s ServerReport) bool { return len(s.ACL) == 1 && s.ACL[0] == sid }) {
		problems = append(problems, "no server denied the wrong account at its ACL")
	}
	if f := decided(ClientStaleClaim, ReasonClaim, sid); f != nil {
		if c := f.entry.Observation.Claim; c == nil || c.PID != f.entry.Observation.PID || c.Created == f.entry.Observation.Created {
			problems = append(problems, "the stale claim was not a stale claim of the held caller")
		}
	}
	if !slices.ContainsFunc(entries, func(f found) bool {
		o := f.entry.Observation
		return f.entry.Verdict.Reason == ReasonExited && o.Exited && o.ProcessSID == sid && o.RequestError == ""
	}) {
		problems = append(problems, "no held caller of the account was refused for exiting")
	}
	return problems
}
