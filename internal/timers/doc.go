// Package timers contains calendar expression parsing and monotonic scheduling.
//
// The scheduler is internal to winunitd (DESIGN.md §17–§18). It does not
// proxy Task Scheduler. OnBootSec is relative to machine boot; OnStartupSec
// is relative to this winunitd instance. Clock.Timer / Fake.Advance drive
// waits without sleeping on the real clock (issue #28).
package timers
