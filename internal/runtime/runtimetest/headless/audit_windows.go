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

	procAuditQuerySystemPolicy = advapi32.NewProc("AuditQuerySystemPolicy")
	procAuditFree              = advapi32.NewProc("AuditFree")
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
	// Security log events: a successful logon, the log being cleared and
	// a change to the system audit policy.
	eventLogon        = 4624
	eventCleared      = 1102
	eventPolicyChange = 4719
	// auditSettle is how long a logged event can take to appear in the
	// log; the history read covers the log until that long before it began.
	auditSettle = 2 * time.Second
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

// auditedLogons reads the Security log's logon history since the boot into
// a: the oldest event the log still holds, whether it was cleared since the
// boot, changes to the logon or audit-policy change subcategories since
// then, the audit policy now, and every password-bearing logon of the
// watched accounts since the boot with its logon process. It waits for
// events to settle and records how far the read covers the log.
func auditedLogons(a *AuditFacts, boot uint64, sids map[string]bool) []LogonFact {
	fail := func(op string, err error) { a.Errors = append(a.Errors, NativeError{Op: op, Win32: win32Code(err)}) }
	var err error
	if a.PolicyEnd, err = auditPolicy(); err != nil {
		fail("audit-policy-end", err)
	}
	time.Sleep(auditSettle)
	a.To = filetimeNow() - uint64(auditSettle/100)
	err = evtEach("*", func(e *auditEvent) (bool, error) {
		at, err := e.at()
		a.Oldest = at
		return false, err
	})
	if err != nil {
		fail("audit-oldest", err)
	}
	watched := map[string]bool{strings.ToLower(subcategoryLogon.String()): true, strings.ToLower(subcategoryPolicyChange.String()): true}
	since := FiletimeTime(boot).Format("2006-01-02T15:04:05.000Z")
	query := fmt.Sprintf("*[System[(EventID=%d or EventID=%d or EventID=%d) and TimeCreated[@SystemTime>='%s']]]", eventLogon, eventCleared, eventPolicyChange, since)
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
			if watched[strings.ToLower(e.field("SubcategoryGuid"))] {
				a.PolicyChanges++
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
