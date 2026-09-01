package runtime

import "testing"

func TestIsClockChangeControl(t *testing.T) {
	t.Parallel()
	if !IsClockChangeControl(ServiceControlTimeChange, 0) {
		t.Fatal("TIMECHANGE must wake calendar timers")
	}
	if !IsClockChangeControl(ServiceControlPowerEvent, PBTAPMResumeAutomatic) {
		t.Fatal("PBT_APMRESUMEAUTOMATIC must wake calendar timers")
	}
	if !IsClockChangeControl(ServiceControlPowerEvent, PBTAPMResumeSuspend) {
		t.Fatal("PBT_APMRESUMESUSPEND must wake calendar timers")
	}
	if IsClockChangeControl(ServiceControlPowerEvent, 0x0004) { // PBT_APMSUSPEND
		t.Fatal("suspend must not be treated as a clock change")
	}
	if IsClockChangeControl(1, 0) { // SERVICE_CONTROL_STOP
		t.Fatal("STOP is not a clock change")
	}
	if IsClockChangeControl(14, PBTAPMResumeAutomatic) { // SESSIONCHANGE
		t.Fatal("SESSIONCHANGE is not a clock change")
	}
}
