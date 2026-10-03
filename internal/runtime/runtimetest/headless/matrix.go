package headless

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Ledgers. C3 holds the approved #254 native groups, B the B01-B04 records
// that refer into it, H the #266 cases.
const (
	LedgerC3 = "C3"
	LedgerB  = "B"
	LedgerH  = "H"
)

// Evidence planes. A daemon record observes the admitted installed daemon; an
// owner-test record comes from a native test that owns what it queries; a
// reference record has no execution and cites other records.
const (
	PlaneDaemon    = "daemon"
	PlaneOwnerTest = "owner-test"
	PlaneReference = "reference"
)

// Account roles: the two disposable standard accounts, a filtered
// administrator, the fixture identity of the SMB password control, and
// SYSTEM.
const (
	AccountA       = "A"
	AccountB       = "B"
	AccountAdmin   = "admin"
	AccountControl = "control"
	AccountSystem  = "system"
)

// Modes name the token a record must have run under.
const (
	ModeS4U           = "s4u"
	ModeWTS           = "wts"
	ModeSystem        = "system"
	ModeFilteredAdmin = "filtered-admin"
	ModePassword      = "password"
	ModePeer          = "peer"
)

// Characterizations of a network or encrypted-file probe.
const (
	CharTCP = "tcp"
	CharSMB = "smb"
	CharEFS = "efs"
)

// Path and pipe sub-result sets.
const (
	PathsOwnRoots         = "own-roots"
	PathsMissingAndDenied = "missing-and-denied"
	// PathsDaemonDenial: the account reads its own daemon log and is denied
	// the peer's and the system manager's daemon directories and logs.
	PathsDaemonDenial = "daemon-denial"
	PipeAuthorized    = "authorized"
	PipeDenied        = "denied"
)

// Metrics a case can require; they are computed from raw evidence.
var knownMetrics = []string{
	"durationSec", "cappedGaps", "shortGapsAfterCap", "grownGapSec", "stableSec", "postResetGapSec",
	"negativeSec", "failures", "maxStartsIn10s", "orderedReplacement", "startLimited", "peerUnchanged", "drained", "kept",
	"maxExitedLifeSec", "recovered", "postResetGrowth", "unlimited", "restartAttempts", "withinBurst",
	"bootStarted", "progressing", "profileCreated", "profileExisting", "interactiveSessions", "sessionCycle", "profileUnloaded",
	"lingerKept", "sessionRetained", "overlongGaps", "failedBeforeQuiet", "cappedBeforeQuiet", "waitingAtQuiet",
	"grewBeforeQuiet",
}

// The observer report derivation each lifecycle metric needs: launches of
// the observed role, a negative window, a replacement, the peer or the
// account's drained processes. startLimited comes from a status snapshot.
var metricNeeds = map[string]string{
	"durationSec": "role", "cappedGaps": "role", "shortGapsAfterCap": "role", "grownGapSec": "role", "stableSec": "role",
	"postResetGapSec": "role", "failures": "role", "maxStartsIn10s": "role", "negativeSec": "negative",
	"orderedReplacement": "crash", "peerUnchanged": "peer", "drained": "drained", "kept": "kept",
	"maxExitedLifeSec": "role", "recovered": "role", "postResetGrowth": "role", "withinBurst": "role",
	"bootStarted": "boot", "progressing": "boot", "profileCreated": "boot", "profileExisting": "boot", "interactiveSessions": "sessions",
	"sessionCycle": "session", "profileUnloaded": "unloaded", "lingerKept": "independent", "sessionRetained": "independent",
	"overlongGaps": "role", "failedBeforeQuiet": "negative", "cappedBeforeQuiet": "negative", "waitingAtQuiet": "negative",
	"grewBeforeQuiet": "negative",
}

//go:embed matrix.json
var matrixJSON []byte

// Matrix is the case table.
type Matrix struct {
	Version int             `json:"version"`
	Phases  []Phase         `json:"phases"`
	Cases   map[string]Case `json:"cases"`
}

// Phase is one execution phase. Every record of a phase precedes every
// record of the next one.
type Phase struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// OneBoot requires all of the phase's records to share one boot.
	OneBoot bool `json:"oneBoot,omitempty"`
	// NoPassword requires no password-bearing logon since that boot.
	NoPassword bool `json:"noPassword,omitempty"`
}

// Case is one case and its variants.
type Case struct {
	Ledger string  `json:"ledger"`
	Plane  string  `json:"plane,omitempty"`
	Phase  int     `json:"phase,omitempty"`
	CapSec float64 `json:"capSec,omitempty"`
	// CapToleranceSec bounds how far past the cap a capped gap may be:
	// the failing process's own life, reconciliation and scheduler slack.
	CapToleranceSec float64       `json:"capToleranceSec,omitempty"`
	Title           string        `json:"title"`
	Expect          string        `json:"expect"`
	Variants        []Variant     `json:"variants"`
	Requires        []Requirement `json:"requires,omitempty"`
	Controls        []ControlDef  `json:"controls,omitempty"`
	Characterize    string        `json:"characterize,omitempty"`
	Paths           string        `json:"paths,omitempty"`
	Pipe            string        `json:"pipe,omitempty"`
	// Runners is the number of fresh test runners whose repetitions each
	// run every variant once.
	Runners int `json:"runners,omitempty"`
	// Observe is what the summary derives from the SYSTEM observer's
	// report; a case with lifecycle requirements needs it.
	Observe *ObserveSpec `json:"observe,omitempty"`
	// Proof is what each record must carry before it is accepted.
	Proof string `json:"proof,omitempty"`
	// Package is the product package whose test binary runs a named test.
	Package string `json:"package,omitempty"`
	// Check is what a daemon-log proof is held to.
	Check string `json:"check,omitempty"`
}

// ObserveSpec names the lifecycle values derived from an observer report.
type ObserveSpec struct {
	// Role is the observed role whose generations are the launches.
	Role string `json:"role,omitempty"`
	// Negative starts the negative window at this mark, or at the
	// observer's first scan for "start"; it ends at the last scan.
	Negative string `json:"negative,omitempty"`
	// Crash is the role the observer terminates; Old are the roles whose
	// processes alive then must exit before the first New process.
	Crash string   `json:"crash,omitempty"`
	Old   []string `json:"old,omitempty"`
	New   string   `json:"new,omitempty"`
	// Kept are roles of the account whose one process must stay the same
	// throughout.
	Kept []string `json:"kept,omitempty"`
	// Peer requires the other account's manager and workload to stay the
	// same processes throughout.
	Peer bool `json:"peer,omitempty"`
	// Drained requires every process of the account to have exited.
	Drained bool `json:"drained,omitempty"`
	// Restart requires the broker to stop and start again inside the
	// negative window, which nonetheless relaunches nothing.
	Restart bool `json:"restart,omitempty"`
	// Subject binds the record's probes to a process the observer held as
	// the account's workload or one of its children.
	Subject bool `json:"subject,omitempty"`
	// Boot derives the cold-boot start, progress and profile ("created" or
	// "existing"); Session the account's interactive session coming and
	// going; Unloaded its profile hive unloaded at the end.
	Boot     string `json:"boot,omitempty"`
	Session  bool   `json:"session,omitempty"`
	Unloaded bool   `json:"unloaded,omitempty"`
	// Independent derives H06's sequence from the marks admission-revoked,
	// linger-disabled and logoff: the headless manager outlives the
	// admission change, a session manager the linger change, until logoff.
	Independent bool `json:"independent,omitempty"`
}

// Variant is one account, mode or subcase of a case.
type Variant struct {
	ID          string   `json:"id"`
	Account     string   `json:"account,omitempty"`
	Mode        string   `json:"mode,omitempty"`
	Plane       string   `json:"plane,omitempty"`
	Phase       int      `json:"phase,omitempty"`
	Execution   string   `json:"execution,omitempty"`
	Refs        []string `json:"refs,omitempty"`
	Repetitions int      `json:"repetitions,omitempty"`
	Test        string   `json:"test,omitempty"`
	// Tests are several named tests of one run.
	Tests   []string     `json:"tests,omitempty"`
	Proof   string       `json:"proof,omitempty"`
	Package string       `json:"package,omitempty"`
	Check   string       `json:"check,omitempty"`
	Paths   string       `json:"paths,omitempty"`
	Observe *ObserveSpec `json:"observe,omitempty"`
}

// ControlDef is a separately keyed control a case's records require.
type ControlDef struct {
	Name    string `json:"name"`
	Mode    string `json:"mode,omitempty"`
	Account string `json:"account,omitempty"`
	Phase   int    `json:"phase,omitempty"`
	// ImmediatelyBefore requires the control to run just before its record,
	// with no other record of that account between them.
	ImmediatelyBefore bool          `json:"immediatelyBefore,omitempty"`
	Requires          []Requirement `json:"requires,omitempty"`
	Proof             string        `json:"proof"`
	Observe           *ObserveSpec  `json:"observe,omitempty"`
	Paths             string        `json:"paths,omitempty"`
}

// Requirement bounds one metric computed from a record's raw evidence.
type Requirement struct {
	Metric string   `json:"metric"`
	Min    *float64 `json:"min,omitempty"`
	Max    *float64 `json:"max,omitempty"`
}

// Entry is one expected record.
type Entry struct {
	Key             string         `json:"key"`
	Case            string         `json:"case"`
	Variant         string         `json:"variant"`
	Repetition      string         `json:"repetition,omitempty"`
	Ledger          string         `json:"ledger"`
	Plane           string         `json:"plane"`
	Account         string         `json:"account,omitempty"`
	Mode            string         `json:"mode,omitempty"`
	Phase           int            `json:"phase,omitempty"`
	Execution       string         `json:"execution,omitempty"`
	Refs            []string       `json:"refs,omitempty"`
	Test            string         `json:"test,omitempty"`
	CapSec          float64        `json:"capSec,omitempty"`
	CapToleranceSec float64        `json:"capToleranceSec,omitempty"`
	Requires        []Requirement  `json:"requires,omitempty"`
	Characterize    string         `json:"characterize,omitempty"`
	Paths           string         `json:"paths,omitempty"`
	Pipe            string         `json:"pipe,omitempty"`
	Controls        []ControlEntry `json:"controls,omitempty"`
	Observe         *ObserveSpec   `json:"observe,omitempty"`
	Proof           string         `json:"proof,omitempty"`
	Package         string         `json:"package,omitempty"`
	Tests           []string       `json:"tests,omitempty"`
	Check           string         `json:"check,omitempty"`
}

// ControlEntry is one expected control record.
type ControlEntry struct {
	Key               string        `json:"key"`
	Name              string        `json:"name"`
	Account           string        `json:"account"`
	Mode              string        `json:"mode"`
	Phase             int           `json:"phase"`
	ImmediatelyBefore bool          `json:"immediatelyBefore,omitempty"`
	Requires          []Requirement `json:"requires,omitempty"`
	Proof             string        `json:"proof"`
	Observe           *ObserveSpec  `json:"observe,omitempty"`
	Paths             string        `json:"paths,omitempty"`
}

var (
	caseIDPattern  = regexp.MustCompile(`^(G[1-6]|B0[1-4]|H(0[1-9]|1[0-9]|2[0-2]))$`)
	nameIDPattern  = regexp.MustCompile(`^[a-z0-9A-Z][a-zA-Z0-9-]{0,31}$`)
	controlPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	testPattern    = regexp.MustCompile(`^Test[A-Za-z0-9]+$`)
	packagePattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,31}$`)
)

// CaseMatrix decodes and validates the embedded matrix.
func CaseMatrix() (*Matrix, error) { return DecodeMatrix(matrixJSON) }

// MatrixHash is the SHA-256 of the embedded matrix bytes.
func MatrixHash() string {
	sum := sha256.Sum256(matrixJSON)
	return hex.EncodeToString(sum[:])
}

// DecodeMatrix strictly decodes and validates a case table and its
// expansion.
func DecodeMatrix(data []byte) (*Matrix, error) {
	var m Matrix
	if err := decodeStrict(data, &m); err != nil {
		return nil, fmt.Errorf("matrix: %w", err)
	}
	if m.Version != 1 {
		return nil, fmt.Errorf("matrix version %d", m.Version)
	}
	for i, p := range m.Phases {
		if p.ID != i+1 || p.Name == "" {
			return nil, fmt.Errorf("phase %d", i+1)
		}
	}
	for id, c := range m.Cases {
		if err := m.validateCase(id, c); err != nil {
			return nil, fmt.Errorf("case %s: %w", id, err)
		}
	}
	entries, err := m.expand()
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for _, e := range entries {
		keys[e.Key] = true
	}
	groups := map[string]int{}
	for _, e := range entries {
		for _, ref := range e.Refs {
			if !keys[ref] || ref == e.Key {
				return nil, fmt.Errorf("%s refers to %q", e.Key, ref)
			}
		}
		if e.Execution != "" {
			groups[e.Execution]++
		}
	}
	for g, n := range groups {
		if n < 2 {
			return nil, fmt.Errorf("shared execution %q has one record", g)
		}
	}
	if _, err := resolveAll(entries); err != nil {
		return nil, err
	}
	return &m, nil
}

func validMode(account, mode string) bool {
	switch mode {
	case ModeS4U, ModeWTS:
		return account == AccountA || account == AccountB
	case ModeSystem:
		return account == AccountSystem
	case ModeFilteredAdmin:
		return account == AccountAdmin
	}
	return false
}

func (m *Matrix) validPhase(p int) bool { return p >= 1 && p <= len(m.Phases) }

func validRequirements(reqs []Requirement) error {
	for _, r := range reqs {
		if !slices.Contains(knownMetrics, r.Metric) || (r.Min == nil && r.Max == nil) {
			return fmt.Errorf("requirement %q", r.Metric)
		}
	}
	return nil
}

func validPaths(set string) bool {
	switch set {
	case "", PathsOwnRoots, PathsMissingAndDenied, PathsDaemonDenial:
		return true
	}
	return false
}

func validRole(role string, broker bool) bool {
	return role == RoleManager || role == RoleWorkload || role == RoleChild || broker && role == RoleBroker
}

// validObserve requires an observer derivation for every lifecycle
// requirement, and only for daemon-plane cases.
func validObserve(c Case) error { return validObserveSpec(c.Observe, c.Requires, c.Plane) }

func validObserveSpec(o *ObserveSpec, requires []Requirement, plane string) error {
	if o == nil {
		for _, r := range requires {
			if metricNeeds[r.Metric] != "" {
				return fmt.Errorf("requirement %s needs an observer derivation", r.Metric)
			}
		}
		return nil
	}
	if plane != PlaneDaemon {
		return errors.New("only daemon cases are observed")
	}
	if o.Role != "" && !validRole(o.Role, false) {
		return fmt.Errorf("observed role %q", o.Role)
	}
	if o.Negative != "" && (!markPattern.MatchString(o.Negative) || o.Role == "") {
		return fmt.Errorf("negative window %q", o.Negative)
	}
	if o.Crash != "" || o.New != "" || len(o.Old) > 0 {
		if !validRole(o.Crash, true) || !validRole(o.New, false) || o.New == RoleChild || len(o.Old) == 0 || !slices.Contains(o.Old, o.Crash) {
			return errors.New("a replacement needs a crashed role among the old roles and a new role")
		}
		for _, r := range o.Old {
			if !validRole(r, true) {
				return fmt.Errorf("old role %q", r)
			}
		}
	}
	for _, r := range o.Kept {
		if !validRole(r, false) {
			return fmt.Errorf("kept role %q", r)
		}
	}
	if o.Restart && o.Negative == "" {
		return errors.New("a broker restart is checked inside a negative window")
	}
	if o.Boot != "" && o.Boot != BootCreated && o.Boot != BootExisting {
		return fmt.Errorf("boot %q", o.Boot)
	}
	has := map[string]bool{"role": o.Role != "", "negative": o.Negative != "", "crash": o.Crash != "", "peer": o.Peer, "drained": o.Drained,
		"kept": len(o.Kept) > 0, "boot": o.Boot != "", "session": o.Session, "unloaded": o.Unloaded, "sessions": o.Boot != "" || o.Session,
		"independent": o.Independent}
	for _, r := range requires {
		if need := metricNeeds[r.Metric]; need != "" && !has[need] {
			return fmt.Errorf("requirement %s needs the observer's %s", r.Metric, need)
		}
	}
	return nil
}

func (m *Matrix) validateCase(id string, c Case) error {
	if !caseIDPattern.MatchString(id) || c.Title == "" || c.Expect == "" || len(c.Variants) == 0 {
		return errors.New("needs an ID, title, expectation and variants")
	}
	wantLedger := map[byte]string{'G': LedgerC3, 'B': LedgerB, 'H': LedgerH}[id[0]]
	if c.Ledger != wantLedger {
		return fmt.Errorf("ledger %q", c.Ledger)
	}
	if err := validRequirements(c.Requires); err != nil {
		return err
	}
	if err := validObserve(c); err != nil {
		return err
	}
	switch c.Characterize {
	case "", CharTCP, CharSMB, CharEFS:
	default:
		return fmt.Errorf("characterize %q", c.Characterize)
	}
	if !validPaths(c.Paths) {
		return fmt.Errorf("paths %q", c.Paths)
	}
	switch c.Pipe {
	case "", PipeAuthorized, PipeDenied:
	default:
		return fmt.Errorf("pipe %q", c.Pipe)
	}
	seenControls := map[string]bool{}
	for _, ctl := range c.Controls {
		if !controlPattern.MatchString(ctl.Name) || seenControls[ctl.Name] {
			return fmt.Errorf("control %q", ctl.Name)
		}
		seenControls[ctl.Name] = true
		switch ctl.Mode {
		case "", ModeSystem, ModePeer, ModePassword:
		default:
			return fmt.Errorf("control %s mode %q", ctl.Name, ctl.Mode)
		}
		if ctl.Account != "" && ctl.Account != AccountControl || ctl.Account == AccountControl && ctl.Mode != ModePassword {
			return fmt.Errorf("control %s account %q", ctl.Name, ctl.Account)
		}
		if ctl.Phase != 0 && !m.validPhase(ctl.Phase) {
			return fmt.Errorf("control %s phase %d", ctl.Name, ctl.Phase)
		}
		if err := validRequirements(ctl.Requires); err != nil {
			return err
		}
		if err := validControlProof(ctl, c.Plane); err != nil {
			return fmt.Errorf("control %s: %w", ctl.Name, err)
		}
	}
	seen := map[string]bool{}
	for _, v := range c.Variants {
		if !nameIDPattern.MatchString(v.ID) || seen[v.ID] {
			return fmt.Errorf("variant %q", v.ID)
		}
		seen[v.ID] = true
		if len(v.Refs) > 0 {
			if v.Account != "" || v.Mode != "" || v.Execution != "" || v.Repetitions != 0 || v.Phase != 0 || v.Test != "" || v.Proof != "" ||
				len(v.Tests) > 0 || v.Package != "" || v.Check != "" || v.Paths != "" || v.Observe != nil {
				return fmt.Errorf("reference variant %s has execution fields", v.ID)
			}
			continue
		}
		if c.Ledger == LedgerB {
			return fmt.Errorf("B variant %s must refer to C3 records", v.ID)
		}
		if !validMode(v.Account, v.Mode) {
			return fmt.Errorf("variant %s account %q mode %q", v.ID, v.Account, v.Mode)
		}
		plane := c.Plane
		if v.Plane != "" {
			plane = v.Plane
		}
		eff := effective(c, v)
		if eff.Observe != nil && plane != PlaneDaemon {
			return fmt.Errorf("variant %s of an observed case is not a daemon record", v.ID)
		}
		if v.Observe != nil {
			if err := validObserveSpec(v.Observe, c.Requires, plane); err != nil {
				return fmt.Errorf("variant %s: %w", v.ID, err)
			}
		}
		if !validPaths(v.Paths) {
			return fmt.Errorf("variant %s paths %q", v.ID, v.Paths)
		}
		if v.Test != "" && len(v.Tests) > 0 {
			return fmt.Errorf("variant %s names a test and a test list", v.ID)
		}
		for _, name := range v.Tests {
			if !testPattern.MatchString(name) {
				return fmt.Errorf("variant %s test %q", v.ID, name)
			}
		}
		if plane != PlaneDaemon && plane != PlaneOwnerTest {
			return fmt.Errorf("variant %s plane %q", v.ID, plane)
		}
		phase := c.Phase
		if v.Phase != 0 {
			phase = v.Phase
		}
		if !m.validPhase(phase) {
			return fmt.Errorf("variant %s phase %d", v.ID, phase)
		}
		if v.Repetitions < 0 || v.Repetitions > 9 || c.Runners != 0 && v.Repetitions != c.Runners {
			return fmt.Errorf("variant %s repetitions %d", v.ID, v.Repetitions)
		}
		if (v.Test != "" || len(v.Tests) > 0) && plane != PlaneOwnerTest || v.Test != "" && !testPattern.MatchString(v.Test) {
			return fmt.Errorf("variant %s test %q", v.ID, v.Test)
		}
		if v.Execution != "" && !controlPattern.MatchString(v.Execution) {
			return fmt.Errorf("variant %s execution %q", v.ID, v.Execution)
		}
		if err := validProof(eff, plane, v); err != nil {
			return fmt.Errorf("variant %s: %w", v.ID, err)
		}
	}
	return nil
}

// variantView is a variant's fields after the case's defaults apply.
type variantView struct {
	Proof, Package, Check, Paths string
	Observe                      *ObserveSpec
}

func effective(c Case, v Variant) variantView {
	pick := func(variant, kase string) string {
		if variant != "" {
			return variant
		}
		return kase
	}
	e := variantView{Proof: pick(v.Proof, c.Proof), Package: pick(v.Package, c.Package), Check: pick(v.Check, c.Check),
		Paths: pick(v.Paths, c.Paths), Observe: c.Observe}
	if v.Observe != nil {
		e.Observe = v.Observe
	}
	return e
}

// validProof requires each executed variant to declare the proof its
// records carry, and the case to define what that proof needs.
func validProof(e variantView, plane string, v Variant) error {
	if e.Check != "" && e.Proof != ProofDaemonLog {
		return errors.New("only a daemon-log proof has a check")
	}
	switch e.Proof {
	case ProofPending:
		return nil
	case ProofObserver:
		if e.Observe == nil {
			return errors.New("an observer proof needs an observer derivation")
		}
	case ProofProbe:
		if e.Observe == nil || !e.Observe.Subject {
			return errors.New("a probe proof needs the observer to hold its subject")
		}
	case ProofNamedTest:
		if plane != PlaneOwnerTest || v.Test == "" && len(v.Tests) == 0 || !packagePattern.MatchString(e.Package) {
			return errors.New("a named-test proof needs an owner test, its names and its package")
		}
	case ProofDaemonLog:
		switch {
		case e.Check == CheckProtection && v.Mode == ModeSystem && e.Observe == nil:
		case (e.Check == CheckRotation || e.Check == CheckRepair) && v.Mode != ModeSystem && e.Observe != nil:
		default:
			return errors.New("a daemon-log proof needs a check that fits its account, and an observer for a manager's start")
		}
	case ProofSessionProbe:
		if v.Mode != ModeWTS && v.Mode != ModeFilteredAdmin || e.Paths == "" {
			return errors.New("a session probe needs a session mode and a path set")
		}
	case ProofInventory:
		if v.Mode != ModeSystem {
			return errors.New("the inventory is SYSTEM's")
		}
	default:
		return fmt.Errorf("proof %q", e.Proof)
	}
	if plane == PlaneOwnerTest && e.Proof != ProofNamedTest {
		return errors.New("an owner test needs a named-test proof")
	}
	return nil
}

// validControlProof requires each control's proof to fit its mode.
func validControlProof(ctl ControlDef, plane string) error {
	if err := validObserveSpec(ctl.Observe, ctl.Requires, plane); err != nil {
		return err
	}
	ok := false
	switch ctl.Proof {
	case ProofPending:
		ok = true
	case ProofFirstUse:
		ok = ctl.Mode == ModeSystem && ctl.ImmediatelyBefore
	case ProofReceipt, ProofPrincipal:
		ok = ctl.Mode == ModePeer
	case ProofPassword:
		ok = ctl.Mode == ModePassword
	case ProofEndpoint:
		ok = ctl.Mode == ModeSystem
	case ProofObserver:
		ok = ctl.Mode == "" && ctl.Observe != nil
	case ProofProbe:
		ok = ctl.Mode == "" && ctl.Observe != nil && ctl.Observe.Subject && (ctl.Paths == PathsOwnRoots || ctl.Paths == PathsMissingAndDenied)
	}
	if !ok {
		return fmt.Errorf("proof %q does not fit the control", ctl.Proof)
	}
	return nil
}

// Expand returns every expected record in case and variant order.
func (m *Matrix) Expand() []Entry {
	entries, _ := m.expand()
	return entries
}

func (m *Matrix) expand() ([]Entry, error) {
	ids := make([]string, 0, len(m.Cases))
	for id := range m.Cases {
		ids = append(ids, id)
	}
	order := map[byte]int{'G': 0, 'B': 1, 'H': 2}
	sort.Slice(ids, func(i, j int) bool {
		if order[ids[i][0]] != order[ids[j][0]] {
			return order[ids[i][0]] < order[ids[j][0]]
		}
		return ids[i] < ids[j]
	})
	var out []Entry
	for _, id := range ids {
		c := m.Cases[id]
		for _, v := range c.Variants {
			e := Entry{Case: id, Variant: v.ID, Ledger: c.Ledger, Account: v.Account, Mode: v.Mode, Execution: v.Execution,
				Refs: slices.Clone(v.Refs), Test: v.Test, CapSec: c.CapSec, CapToleranceSec: c.CapToleranceSec, Requires: c.Requires,
				Characterize: c.Characterize, Paths: c.Paths, Pipe: c.Pipe, Observe: c.Observe, Proof: c.Proof, Package: c.Package}
			eff := effective(c, v)
			e.Proof, e.Package, e.Check, e.Paths, e.Observe, e.Tests = eff.Proof, eff.Package, eff.Check, eff.Paths, eff.Observe, slices.Clone(v.Tests)
			if len(v.Refs) > 0 {
				e.Plane = PlaneReference
				e.Key = id + "/" + v.ID
				e.Requires, e.Characterize, e.Paths, e.Pipe, e.CapSec, e.CapToleranceSec, e.Observe, e.Proof, e.Package, e.Check, e.Tests = nil, "", "", "", 0, 0, nil, "", "", "", nil
				out = append(out, e)
				continue
			}
			e.Plane, e.Phase = c.Plane, c.Phase
			if v.Plane != "" {
				e.Plane = v.Plane
			}
			if v.Phase != 0 {
				e.Phase = v.Phase
			}
			reps := []string{""}
			if v.Repetitions > 1 {
				reps = nil
				for r := 1; r <= v.Repetitions; r++ {
					reps = append(reps, fmt.Sprintf("r%d", r))
				}
			}
			for _, rep := range reps {
				x := e
				x.Repetition = rep
				x.Key = id + "/" + v.ID
				if rep != "" {
					x.Key += "/" + rep
				}
				for _, ctl := range c.Controls {
					ce := ControlEntry{Key: x.Key + "#" + ctl.Name, Name: ctl.Name, Account: x.Account, Mode: x.Mode, Phase: x.Phase,
						ImmediatelyBefore: ctl.ImmediatelyBefore, Requires: ctl.Requires, Proof: ctl.Proof, Observe: ctl.Observe, Paths: ctl.Paths}
					if ctl.Mode != "" {
						ce.Mode = ctl.Mode
					}
					if ctl.Account != "" {
						ce.Account = ctl.Account
					}
					if ctl.Mode == ModeSystem {
						ce.Account = AccountSystem
					}
					if ctl.Mode == ModePeer {
						ce.Account = ""
					}
					if ctl.Phase != 0 {
						ce.Phase = ctl.Phase
					}
					x.Controls = append(x.Controls, ce)
				}
				out = append(out, x)
			}
		}
	}
	return out, nil
}

// resolveAll follows every reference to the executed records it cites and
// rejects a cycle or a reference across accounts.
func resolveAll(entries []Entry) (map[string][]string, error) {
	byKey := map[string]Entry{}
	for _, e := range entries {
		byKey[e.Key] = e
	}
	out := map[string][]string{}
	var resolve func(key string, depth int) ([]string, error)
	resolve = func(key string, depth int) ([]string, error) {
		if depth > 8 {
			return nil, fmt.Errorf("reference cycle at %s", key)
		}
		e := byKey[key]
		if e.Plane != PlaneReference {
			return []string{key}, nil
		}
		var all []string
		for _, ref := range e.Refs {
			got, err := resolve(ref, depth+1)
			if err != nil {
				return nil, err
			}
			all = append(all, got...)
		}
		return all, nil
	}
	for _, e := range entries {
		if e.Plane != PlaneReference {
			continue
		}
		got, err := resolve(e.Key, 0)
		if err != nil {
			return nil, err
		}
		account := ""
		for _, k := range got {
			a := byKey[k].Account
			if account == "" {
				account = a
			}
			// A reference that spans both accounts cites one shared event.
			if a != account && byKey[k].Execution == "" {
				return nil, fmt.Errorf("%s mixes accounts", e.Key)
			}
		}
		out[e.Key] = got
	}
	return out, nil
}

// Counts reports the expected records per ledger and in total.
func (m *Matrix) Counts() map[string]int {
	out := map[string]int{}
	for _, e := range m.Expand() {
		out[e.Ledger]++
		out["total"]++
	}
	return out
}

// Selection limits a development run. Any non-empty selection is partial.
type Selection struct {
	Ledgers  []string
	Cases    []string
	Accounts []string
	Phases   []int
}

// Empty reports whether s selects everything.
func (s Selection) Empty() bool {
	return len(s.Ledgers) == 0 && len(s.Cases) == 0 && len(s.Accounts) == 0 && len(s.Phases) == 0
}

// Select returns the entries in s.
func (m *Matrix) Select(s Selection) []Entry {
	var out []Entry
	for _, e := range m.Expand() {
		if len(s.Ledgers) > 0 && !slices.Contains(s.Ledgers, e.Ledger) ||
			len(s.Cases) > 0 && !slices.Contains(s.Cases, e.Case) ||
			len(s.Accounts) > 0 && !slices.Contains(s.Accounts, e.Account) ||
			len(s.Phases) > 0 && !slices.Contains(s.Phases, e.Phase) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// primaryOf returns the primary key of a control key.
func primaryOf(key string) string {
	k, _, _ := strings.Cut(key, "#")
	return k
}
