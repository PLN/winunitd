// Package winevt is the Windows Application event channel for daemon
// lifecycle and startup failures. Registration is owned by the product MSI.
// The message table embedded in winunitd.exe renders these IDs. The daemon
// log remains the durable file record; this package does not store another copy.
package winevt

import (
	"strings"

	"github.com/PLN/winunitd/internal/journal"
)

// Source is the Application log event source.
const Source = "winunitd"

// Event IDs match the message resource compiled into winunitd.exe.
const (
	IDOpen              uint32 = 1000
	IDClose             uint32 = 1001
	IDStartupFailed     uint32 = 1002
	IDLifecycleRejected uint32 = 1003
	IDStartLimit        uint32 = 1004
)

// Kind is the event severity passed to ReportEvent. It is independent of the
// message ID that selects the rendered text.
type Kind uint16

const (
	KindInfo Kind = iota + 1
	KindWarning
	KindError
)

// Template is one message-table entry. Text may contain %1 and a trailing
// newline, matching the message-compiler layout embedded in the executable.
type Template struct {
	ID   uint32
	Text string
}

// Templates is the ordered message resource. IDs are contiguous so they form
// one message-table block.
func Templates() []Template {
	return []Template{
		{ID: IDOpen, Text: "winunitd daemon opened.\n"},
		{ID: IDClose, Text: "winunitd daemon closed.\n"},
		{ID: IDStartupFailed, Text: "winunitd startup failed. %1\n"},
		{ID: IDLifecycleRejected, Text: "winunitd rejected a lifecycle transition. %1\n"},
		{ID: IDStartLimit, Text: "winunitd reached the start limit. %1\n"},
	}
}

// Render applies insertion strings the way Event Viewer substitutes %1–%9.
// A missing insertion is left blank. %% becomes %.
func Render(id uint32, inserts ...string) (string, bool) {
	var tmpl string
	for _, t := range Templates() {
		if t.ID == id {
			tmpl = t.Text
			break
		}
	}
	if tmpl == "" {
		return "", false
	}
	return substitute(tmpl, inserts), true
}

func substitute(tmpl string, inserts []string) string {
	var b strings.Builder
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] != '%' || i+1 >= len(tmpl) {
			b.WriteByte(tmpl[i])
			continue
		}
		switch n := tmpl[i+1]; {
		case n == '%':
			b.WriteByte('%')
			i++
		case n >= '1' && n <= '9':
			idx := int(n - '1')
			if idx < len(inserts) {
				b.WriteString(inserts[idx])
			}
			i++
		default:
			b.WriteByte('%')
		}
	}
	return b.String()
}

// Format maps one accepted daemon-log view to an event. Unknown codes return
// id 0. Insertion text uses the same public-detail rules as the daemon log.
func Format(view journal.DaemonEventView) (id uint32, kind Kind, insert string) {
	switch view.Code {
	case journal.DaemonEventOpen:
		return IDOpen, KindInfo, ""
	case journal.DaemonEventClose:
		return IDClose, KindInfo, ""
	case journal.DaemonEventStartupFailed:
		return IDStartupFailed, KindError, detail(view.Reason)
	case journal.DaemonEventLifecycleRejected:
		return IDLifecycleRejected, KindWarning, detail(joinDetail(view.Unit, view.Reason))
	case journal.DaemonEventStartLimit:
		return IDStartLimit, KindWarning, detail(startLimitDetail(view))
	default:
		return 0, 0, ""
	}
}

func detail(s string) string {
	cleaned, ok := journal.PublicDetail(s)
	if !ok {
		return ""
	}
	return cleaned
}

func joinDetail(unit, reason string) string {
	unit = strings.TrimSpace(unit)
	reason = strings.TrimSpace(reason)
	switch {
	case unit == "":
		return reason
	case reason == "":
		return unit
	default:
		return unit + " " + reason
	}
}

func startLimitDetail(view journal.DaemonEventView) string {
	var parts []string
	if view.Unit != "" {
		parts = append(parts, "unit="+view.Unit)
	}
	if view.RestartAttempt != 0 {
		parts = append(parts, "attempt="+itoa(view.RestartAttempt))
	}
	if view.StartLimitBurst != nil {
		parts = append(parts, "burst="+itoaSigned(*view.StartLimitBurst))
	}
	if view.StartLimitRemaining != nil {
		parts = append(parts, "remaining="+itoaSigned(*view.StartLimitRemaining))
	}
	return strings.Join(parts, " ")
}

func itoa(n uint32) string {
	if n == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func itoaSigned(n int) string {
	if n < 0 {
		return "-" + itoa(uint32(-n))
	}
	return itoa(uint32(n))
}
