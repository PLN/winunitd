//go:build windows

package headless

import (
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	wevtapi       = windows.NewLazySystemDLL("wevtapi.dll")
	procEvtQuery  = wevtapi.NewProc("EvtQuery")
	procEvtNext   = wevtapi.NewProc("EvtNext")
	procEvtRender = wevtapi.NewProc("EvtRender")
	procEvtClose  = wevtapi.NewProc("EvtClose")

	procAuditQuerySystemPolicy           = advapi32.NewProc("AuditQuerySystemPolicy")
	procAuditComputeEffectivePolicyBySid = advapi32.NewProc("AuditComputeEffectivePolicyBySid")
	procAuditFree                        = advapi32.NewProc("AuditFree")
	procLogonUserW                       = advapi32.NewProc("LogonUserW")
)

// The advanced audit policy subcategories the logon history depends on:
// Logon, and Audit Policy Change, under which a change to either is
// logged as event 4719.
var (
	subcategoryLogon        = windows.GUID{Data1: 0x0cce9215, Data2: 0x69ae, Data3: 0x11d9, Data4: [8]byte{0xbe, 0xd3, 0x50, 0x50, 0x54, 0x50, 0x30, 0x30}}
	subcategoryPolicyChange = windows.GUID{Data1: 0x0cce922f, Data2: 0x69ae, Data3: 0x11d9, Data4: [8]byte{0xbe, 0xd3, 0x50, 0x50, 0x54, 0x50, 0x30, 0x30}}
)

// auditPolicyInformation is AUDIT_POLICY_INFORMATION.
type auditPolicyInformation struct {
	SubCategory         windows.GUID
	AuditingInformation uint32
	Category            windows.GUID
}

const policyAuditSuccess = 0x1

// auditPolicy reads the system audit policy of the logon and audit-policy
// change subcategories.
func auditPolicy() (*AuditPolicy, error) {
	if err := enablePrivilege("SeSecurityPrivilege"); err != nil {
		return nil, err
	}
	guids := []windows.GUID{subcategoryLogon, subcategoryPolicyChange}
	var out *auditPolicyInformation
	r, _, err := procAuditQuerySystemPolicy.Call(uintptr(unsafe.Pointer(&guids[0])), uintptr(len(guids)), uintptr(unsafe.Pointer(&out)))
	if byte(r) == 0 || out == nil {
		return nil, fmt.Errorf("query the audit policy: %w", err)
	}
	defer procAuditFree.Call(uintptr(unsafe.Pointer(out)))
	info := unsafe.Slice(out, len(guids))
	if info[0].SubCategory != guids[0] || info[1].SubCategory != guids[1] {
		return nil, errors.New("the audit policy names other subcategories")
	}
	return &AuditPolicy{LogonSuccess: info[0].AuditingInformation&policyAuditSuccess != 0,
		PolicyChangeSuccess: info[1].AuditingInformation&policyAuditSuccess != 0}, nil
}

// effectiveLogonAudit reports whether an account's successful logons are
// audited under its effective policy, the system policy combined with the
// account's per-user policy.
func effectiveLogonAudit(sid string) (bool, error) {
	if err := enablePrivilege("SeSecurityPrivilege"); err != nil {
		return false, err
	}
	s, err := windows.StringToSid(sid)
	if err != nil {
		return false, err
	}
	guid := subcategoryLogon
	var out *auditPolicyInformation
	r, _, err := procAuditComputeEffectivePolicyBySid.Call(uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(&guid)), 1, uintptr(unsafe.Pointer(&out)))
	if byte(r) == 0 || out == nil {
		return false, fmt.Errorf("compute the effective audit policy: %w", err)
	}
	defer procAuditFree.Call(uintptr(unsafe.Pointer(out)))
	if out.SubCategory != guid {
		return false, errors.New("the effective policy names another subcategory")
	}
	return out.AuditingInformation&policyAuditSuccess != 0, nil
}

// accountAudits records each watched account's effective logon auditing at
// the start or the end into a.
func accountAudits(a *AuditFacts, sids []string, end bool) {
	if a.Accounts == nil {
		a.Accounts = map[string]AccountAudit{}
	}
	for _, sid := range sids {
		ok, err := effectiveLogonAudit(sid)
		if err != nil {
			op := "audit-account-start"
			if end {
				op = "audit-account-end"
			}
			a.Errors = append(a.Errors, NativeError{Op: op, Win32: win32Code(err)})
		}
		acct := a.Accounts[sid]
		if end {
			acct.End = ok
		} else {
			acct.Start = ok
		}
		a.Accounts[sid] = acct
	}
}

// auditMarker makes a service logon of the local service account, an
// attributable successful logon, and waits until the Security log holds its
// audit event: the log then holds every logon the audit path wrote before
// it.
func auditMarker() (*AuditMarker, error) {
	user, _ := windows.UTF16PtrFromString("LocalService")
	domain, _ := windows.UTF16PtrFromString("NT AUTHORITY")
	m := &AuditMarker{Requested: filetimeNow()}
	var tok windows.Token
	r, _, err := procLogonUserW.Call(uintptr(unsafe.Pointer(user)), uintptr(unsafe.Pointer(domain)), 0, logon32LogonService, logon32ProviderDefault,
		uintptr(unsafe.Pointer(&tok)))
	if r == 0 {
		return nil, fmt.Errorf("marker logon: %w", err)
	}
	var stats tokenStatistics
	var n uint32
	err = windows.GetTokenInformation(tok, windows.TokenStatistics, (*byte)(unsafe.Pointer(&stats)), uint32(unsafe.Sizeof(stats)), &n)
	_ = tok.Close()
	if err != nil {
		return nil, err
	}
	id := uint64(uint32(stats.AuthenticationID.HighPart))<<32 | uint64(stats.AuthenticationID.LowPart)
	m.LogonID = fmt.Sprintf("%08x:%08x", id>>32, id&0xffffffff)
	query := fmt.Sprintf("*[System[EventID=%d] and EventData[Data[@Name='TargetLogonId']='0x%x']]", eventLogon, id)
	deadline := time.Now().Add(markerWait)
	for m.Logged == 0 {
		err := evtEach(query, func(e *auditEvent) (bool, error) {
			at, err := e.at()
			m.Logged = at
			return false, err
		})
		if err != nil {
			return nil, err
		}
		if m.Logged == 0 {
			if time.Now().After(deadline) {
				return nil, errors.New("the marker logon did not appear in the Security log")
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	return m, nil
}

// enablePrivilege enables a privilege the process token holds.
func enablePrivilege(name string) error {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, p, &luid); err != nil {
		return err
	}
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return err
	}
	defer tok.Close()
	tp := windows.Tokenprivileges{PrivilegeCount: 1, Privileges: [1]windows.LUIDAndAttributes{{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}}}
	if err := windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil); err != nil {
		return err
	}
	// The call succeeds without enabling a privilege the token lacks.
	return privilegeEnabled(tok, luid)
}

// privilegeEnabled reports an error unless the token's privilege is
// enabled.
func privilegeEnabled(tok windows.Token, luid windows.LUID) error {
	var n uint32
	_ = windows.GetTokenInformation(tok, windows.TokenPrivileges, nil, 0, &n)
	if n == 0 {
		return errors.New("read the token's privileges")
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(tok, windows.TokenPrivileges, &buf[0], n, &n); err != nil {
		return err
	}
	tp := (*windows.Tokenprivileges)(unsafe.Pointer(&buf[0]))
	for _, a := range tp.AllPrivileges() {
		if a.Luid == luid && a.Attributes&windows.SE_PRIVILEGE_ENABLED != 0 {
			return nil
		}
	}
	return windows.ERROR_NOT_ALL_ASSIGNED
}

const (
	evtQueryChannelPath      = 0x1
	evtQueryForwardDirection = 0x100
	evtRenderEventXML        = 1
	evtBatch                 = 64
	// maxAuditEvents bounds what one history read examines.
	maxAuditEvents = 1 << 20
	// Security log events: a successful logon, the log being cleared, a
	// change to the system audit policy and a change to a user's per-user
	// audit policy.
	eventLogon            = 4624
	eventCleared          = 1102
	eventPolicyChange     = 4719
	eventUserPolicyChange = 4912
	// markerWait bounds how long the observer waits for its marker logon
	// to appear in the log; one that does not leaves the history unknown.
	markerWait = 2 * time.Minute
	// Logon types and provider for the marker's service logon.
	logon32LogonService    = 5
	logon32ProviderDefault = 0
)

// auditEvent is the part of a rendered Security event the history reads.
type auditEvent struct {
	System struct {
		EventID     int `xml:"EventID"`
		TimeCreated struct {
			SystemTime string `xml:"SystemTime,attr"`
		} `xml:"TimeCreated"`
	} `xml:"System"`
	Data []struct {
		Name  string `xml:"Name,attr"`
		Value string `xml:",chardata"`
	} `xml:"EventData>Data"`
}

func (e *auditEvent) field(name string) string {
	for _, d := range e.Data {
		if d.Name == name {
			return strings.TrimSpace(d.Value)
		}
	}
	return ""
}

func (e *auditEvent) at() (uint64, error) {
	t, err := time.Parse(time.RFC3339Nano, e.System.TimeCreated.SystemTime)
	if err != nil {
		return 0, err
	}
	return uint64(t.UnixNano()/100) + filetimeEpoch, nil
}

// evtEach runs fn on each event a Security log query returns, rendered as
// XML, oldest first, until fn returns false.
func evtEach(query string, fn func(*auditEvent) (bool, error)) error {
	channel, _ := windows.UTF16PtrFromString("Security")
	q, err := windows.UTF16PtrFromString(query)
	if err != nil {
		return err
	}
	h, _, callErr := procEvtQuery.Call(0, uintptr(unsafe.Pointer(channel)), uintptr(unsafe.Pointer(q)), evtQueryChannelPath|evtQueryForwardDirection)
	if h == 0 {
		return fmt.Errorf("query the Security log: %w", callErr)
	}
	defer procEvtClose.Call(h)
	events := make([]windows.Handle, evtBatch)
	seen := 0
	for {
		var n uint32
		r, _, callErr := procEvtNext.Call(h, evtBatch, uintptr(unsafe.Pointer(&events[0])), 1000, 0, uintptr(unsafe.Pointer(&n)))
		if r == 0 {
			if errors.Is(callErr, windows.ERROR_NO_MORE_ITEMS) {
				return nil
			}
			return fmt.Errorf("read the Security log: %w", callErr)
		}
		stop := false
		var ferr error
		for _, ev := range events[:n] {
			if !stop && ferr == nil {
				var e auditEvent
				if ferr = renderEvent(ev, &e); ferr == nil {
					var more bool
					more, ferr = fn(&e)
					stop = !more
				}
			}
			procEvtClose.Call(uintptr(ev))
		}
		if ferr != nil || stop {
			return ferr
		}
		if seen += int(n); seen > maxAuditEvents {
			return errors.New("too many Security log events")
		}
	}
}

func renderEvent(ev windows.Handle, out *auditEvent) error {
	var used, props uint32
	procEvtRender.Call(0, uintptr(ev), evtRenderEventXML, 0, 0, uintptr(unsafe.Pointer(&used)), uintptr(unsafe.Pointer(&props)))
	if used == 0 {
		return errors.New("render a Security event")
	}
	buf := make([]uint16, used/2+1)
	if r, _, err := procEvtRender.Call(0, uintptr(ev), evtRenderEventXML, uintptr(len(buf)*2), uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&used)), uintptr(unsafe.Pointer(&props))); r == 0 {
		return fmt.Errorf("render a Security event: %w", err)
	}
	return xml.Unmarshal([]byte(windows.UTF16ToString(buf)), out)
}

// logonID turns an audited logon ID such as 0x3e7 into the form LSA
// samples use.
func logonID(s string) (string, bool) {
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(s), "0x"), 16, 64)
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%08x:%08x", v>>32, v&0xffffffff), true
}

// auditedLogons completes the Security log's logon history since the boot
// in a, after the final scan: the system policy and each watched account's
// effective policy now, the marker logon that bounds the collection, then,
// once the marker is in the log, the oldest event the log holds, whether it
// was cleared since the boot, changes to the logon or audit-policy change
// subcategories and to the watched accounts' per-user policy since then,
// and every password-bearing logon of the watched accounts since the boot
// with its logon process.
func auditedLogons(a *AuditFacts, boot uint64, sids map[string]bool) []LogonFact {
	fail := func(op string, err error) { a.Errors = append(a.Errors, NativeError{Op: op, Win32: win32Code(err)}) }
	var err error
	if a.PolicyEnd, err = auditPolicy(); err != nil {
		fail("audit-policy-end", err)
	}
	watched := make([]string, 0, len(sids))
	for sid := range sids {
		watched = append(watched, sid)
	}
	slices.Sort(watched)
	accountAudits(a, watched, true)
	if a.Marker, err = auditMarker(); err != nil {
		fail("audit-marker", err)
	} else {
		a.To = a.Marker.Logged
	}
	err = evtEach("*", func(e *auditEvent) (bool, error) {
		at, err := e.at()
		a.Oldest = at
		return false, err
	})
	if err != nil {
		fail("audit-oldest", err)
	}
	subcategories := map[string]bool{strings.ToLower(subcategoryLogon.String()): true, strings.ToLower(subcategoryPolicyChange.String()): true}
	since := FiletimeTime(boot).Format("2006-01-02T15:04:05.000Z")
	query := fmt.Sprintf("*[System[(EventID=%d or EventID=%d or EventID=%d or EventID=%d) and TimeCreated[@SystemTime>='%s']]]",
		eventLogon, eventCleared, eventPolicyChange, eventUserPolicyChange, since)
	var logons []LogonFact
	err = evtEach(query, func(e *auditEvent) (bool, error) {
		at, err := e.at()
		if err != nil {
			return false, err
		}
		switch e.System.EventID {
		case eventCleared:
			a.ClearedSinceBoot = true
		case eventPolicyChange:
			if subcategories[strings.ToLower(e.field("SubcategoryGuid"))] {
				a.PolicyChanges++
			}
		case eventUserPolicyChange:
			if sids[e.field("TargetUserSid")] {
				a.UserPolicyChanges++
			}
		case eventLogon:
			sid := e.field("TargetUserSid")
			kind, err := strconv.ParseUint(e.field("LogonType"), 10, 32)
			if err != nil || !sids[sid] || !slices.Contains(passwordLogonTypes, uint32(kind)) {
				return true, nil
			}
			id, ok := logonID(e.field("TargetLogonId"))
			if !ok {
				return false, errors.New("an audited logon has no logon ID")
			}
			logons = append(logons, LogonFact{ID: id, SID: sid, Type: uint32(kind), LogonTime: at, Source: "audit",
				Process: strings.TrimSpace(e.field("LogonProcessName"))})
		}
		return true, nil
	})
	if err != nil {
		fail("audit-logons", err)
	}
	a.Read = len(a.Errors) == 0
	return logons
}
