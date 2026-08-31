package notify

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	// EnvNotifyPipe is injected for Type=notify and WatchdogSec= (DESIGN.md §80).
	EnvNotifyPipe = "WINUNIT_NOTIFY_PIPE"
	// EnvWatchdogUsec is injected when WatchdogSec= is set (DESIGN.md §80).
	EnvWatchdogUsec = "WINUNIT_WATCHDOG_USEC"

	pipePrefix = `\\.\pipe\winunitd\notify\`
)

// Message is one sd_notify-shaped payload. Unknown keys are ignored.
type Message struct {
	Ready    bool
	Watchdog bool
	Status   string
	MainPID  int
	HasPID   bool
}

// PipeName is \\.\pipe\winunitd\notify\<unit-id> (unit name until P6).
func PipeName(unitID string) string {
	return pipePrefix + sanitizeUnitID(unitID)
}

func sanitizeUnitID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range id {
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// WatchdogUsec is WatchdogSec as a microsecond decimal string.
func WatchdogUsec(d int64) string {
	return strconv.FormatInt(d, 10)
}

// Parse reads newline-separated KEY=VALUE fields. Unknown keys are ignored.
func Parse(data string) Message {
	var m Message
	data = strings.ReplaceAll(data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")
	for _, line := range strings.Split(data, "\n") {
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "READY":
			if val == "1" {
				m.Ready = true
			}
		case "WATCHDOG":
			if val == "1" {
				m.Watchdog = true
			}
		case "STATUS":
			m.Status = val
		case "MAINPID":
			n, err := strconv.Atoi(val)
			if err == nil && n > 0 {
				m.MainPID = n
				m.HasPID = true
			}
		}
	}
	return m
}

// Format writes the sd_notify-shaped payload, always newline-terminated.
func Format(m Message) string {
	var b strings.Builder
	if m.Ready {
		b.WriteString("READY=1\n")
	}
	if m.Watchdog {
		b.WriteString("WATCHDOG=1\n")
	}
	if m.Status != "" {
		b.WriteString("STATUS=")
		b.WriteString(m.Status)
		b.WriteString("\n")
	}
	if m.HasPID || m.MainPID > 0 {
		fmt.Fprintf(&b, "MAINPID=%d\n", m.MainPID)
	}
	return b.String()
}

// ReadLines scans r and calls emit for each parsed line (and a final
// payload if the stream ends without a trailing newline).
func ReadLines(r io.Reader, emit func(Message)) error {
	if emit == nil {
		return fmt.Errorf("nil emit")
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		msg := Parse(line)
		if msg.Ready || msg.Watchdog || msg.Status != "" || msg.HasPID {
			emit(msg)
		}
	}
	return sc.Err()
}
