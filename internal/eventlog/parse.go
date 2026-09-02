package eventlog

import (
	"fmt"
	"strconv"
	"strings"
)

// Trigger is one EventLogTrigger= value.
type Trigger struct {
	Channel string
	EventID uint16
	Raw     string
}

const triggerSep = ":EventID="

// ParseTrigger parses <Channel>:EventID=<uint16>. EventID=0 is rejected.
func ParseTrigger(raw string) (Trigger, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Trigger{}, fmt.Errorf("empty EventLogTrigger")
	}
	i := strings.LastIndex(s, triggerSep)
	if i < 0 {
		return Trigger{}, fmt.Errorf("EventLogTrigger must be <Channel>:EventID=<uint16>")
	}
	channel := strings.TrimSpace(s[:i])
	idStr := strings.TrimSpace(s[i+len(triggerSep):])
	if channel == "" {
		return Trigger{}, fmt.Errorf("empty channel")
	}
	if idStr == "" {
		return Trigger{}, fmt.Errorf("EventID must be a number")
	}
	n, err := strconv.ParseUint(idStr, 10, 16)
	if err != nil {
		return Trigger{}, fmt.Errorf("EventID must be a number")
	}
	if n == 0 {
		return Trigger{}, fmt.Errorf("EventID=0 is not allowed")
	}
	return Trigger{Channel: channel, EventID: uint16(n), Raw: strings.TrimSpace(raw)}, nil
}

// RestrictedInUserScope reports System or Security (case-insensitive).
// A user manager verify allows Application and custom names only.
func (t Trigger) RestrictedInUserScope() bool {
	switch {
	case strings.EqualFold(t.Channel, "System"):
		return true
	case strings.EqualFold(t.Channel, "Security"):
		return true
	default:
		return false
	}
}

// Query is the EvtSubscribe XPath for this EventID. It is not accepted
// in the unit file; it is only used to implement EventID= on the wire.
// EventID=0 returns "" (OpenSubscribe must not pass Query=NULL).
func (t Trigger) Query() string {
	if t.EventID == 0 {
		return ""
	}
	return fmt.Sprintf("*[System[(EventID=%d)]]", t.EventID)
}
