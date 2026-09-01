// Package timers contains calendar expression parsing and monotonic scheduling.
//
// The scheduler is internal to winunitd (DESIGN.md §17–§18). It does not
// proxy Task Scheduler. OnBootSec is relative to machine boot; OnStartupSec
// is relative to this winunitd instance (monotonic SinceStart). Clock.Timer /
// Fake.Advance drive waits without sleeping on the real clock (issue #28).
// Engine.ClockChanged recomputes OnCalendar / OnUnitActiveSec deadlines after
// a wall jump and wakes the loop; OnBootSec heap entries are not rewritten.
// A 30s poll remains as fallback when the host does not deliver TIMECHANGE.
package timers
