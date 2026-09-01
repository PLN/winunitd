package runtime

// SCM / power constants that golang.org/x/sys/windows/svc does not map
// (winsvc.h / winuser.h). SERVICE_ACCEPT_TIMECHANGE is 0x200.
const (
	ServiceControlPowerEvent = 13
	ServiceControlTimeChange = 16
	ServiceAcceptPowerEvent  = 64
	ServiceAcceptTimeChange  = 0x200

	// PBT_APMRESUMEAUTOMATIC is sent on every resume; PBT_APMRESUMESUSPEND
	// follows a user-triggered suspend. Either means wall time may have jumped.
	PBTAPMResumeAutomatic = 0x0012
	PBTAPMResumeSuspend   = 0x0007
)

// IsClockChangeControl reports whether an SCM control should wake calendar
// timers (DESIGN.md §18). TIMECHANGE always does. POWEREVENT does only on
// resume (PBT_APMRESUMEAUTOMATIC / PBT_APMRESUMESUSPEND).
func IsClockChangeControl(cmd, eventType uint32) bool {
	switch cmd {
	case ServiceControlTimeChange:
		return true
	case ServiceControlPowerEvent:
		switch eventType {
		case PBTAPMResumeAutomatic, PBTAPMResumeSuspend:
			return true
		}
	}
	return false
}
