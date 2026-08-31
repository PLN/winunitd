package core

import "fmt"

// State is a unit lifecycle state (DESIGN.md §68).
type State int

const (
	Inactive State = iota
	Activating
	Active
	Deactivating
	Failed
)

func (s State) String() string {
	switch s {
	case Inactive:
		return "inactive"
	case Activating:
		return "activating"
	case Active:
		return "active"
	case Deactivating:
		return "deactivating"
	case Failed:
		return "failed"
	default:
		return fmt.Sprintf("state(%d)", int(s))
	}
}

// Substate is a lifecycle substate (DESIGN.md §68). Protocol ActiveState
// stays the top-level State string; auto-restart is Activating+SubAutoRestart.
type Substate int

const (
	SubNone Substate = iota
	SubStartPre
	SubStart
	SubStartPost
	SubRunning
	SubStop
	SubStopPost
	SubAutoRestart
	SubWatchdog
)

func (s Substate) String() string {
	switch s {
	case SubNone:
		return ""
	case SubStartPre:
		return "start-pre"
	case SubStart:
		return "start"
	case SubStartPost:
		return "start-post"
	case SubRunning:
		return "running"
	case SubStop:
		return "stop"
	case SubStopPost:
		return "stop-post"
	case SubAutoRestart:
		return "auto-restart"
	case SubWatchdog:
		return "watchdog"
	default:
		return fmt.Sprintf("substate(%d)", int(s))
	}
}

// Event is a lifecycle event applied by Step.
type Event int

const (
	EventStartRequested Event = iota
	EventStartSucceeded
	EventStartFailed
	EventStopRequested
	EventStopFinished
	EventMainExited
	EventAutoRestart
	EventRestartCancelled
	EventWatchdogFailed
)

func (e Event) String() string {
	switch e {
	case EventStartRequested:
		return "start-requested"
	case EventStartSucceeded:
		return "start-succeeded"
	case EventStartFailed:
		return "start-failed"
	case EventStopRequested:
		return "stop-requested"
	case EventStopFinished:
		return "stop-finished"
	case EventMainExited:
		return "main-exited"
	case EventAutoRestart:
		return "auto-restart"
	case EventRestartCancelled:
		return "restart-cancelled"
	case EventWatchdogFailed:
		return "watchdog-failed"
	default:
		return fmt.Sprintf("event(%d)", int(e))
	}
}

// Step applies a lifecycle event. This is not a collection of booleans:
// each event maps to one (state, substate) pair.
func Step(from State, fromSub Substate, ev Event) (State, Substate) {
	switch ev {
	case EventStartRequested:
		return Activating, SubStart
	case EventStartSucceeded:
		return Active, SubRunning
	case EventStartFailed:
		return Failed, SubNone
	case EventStopRequested:
		return Deactivating, SubStop
	case EventStopFinished:
		return Inactive, SubNone
	case EventMainExited:
		return Failed, SubNone
	case EventAutoRestart:
		return Activating, SubAutoRestart
	case EventRestartCancelled:
		return Inactive, SubNone
	case EventWatchdogFailed:
		return Failed, SubWatchdog
	default:
		return from, fromSub
	}
}
