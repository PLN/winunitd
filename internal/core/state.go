package core

import (
	"errors"
	"fmt"
)

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

// ErrIllegalTransition is returned by Step when (from, fromSub, event)
// is not in the explicit allowlist (DESIGN.md §68).
var ErrIllegalTransition = errors.New("illegal lifecycle transition")

// TransitionError names a rejected Step.
type TransitionError struct {
	From    State
	FromSub Substate
	Event   Event
}

func (e *TransitionError) Error() string {
	if e == nil {
		return ErrIllegalTransition.Error()
	}
	if sub := e.FromSub.String(); sub != "" {
		return fmt.Sprintf("illegal transition: %s/%s + %s", e.From, sub, e.Event)
	}
	return fmt.Sprintf("illegal transition: %s + %s", e.From, e.Event)
}

func (e *TransitionError) Unwrap() error {
	return ErrIllegalTransition
}

// Step applies a lifecycle event. from and fromSub are real: transitions
// not in the allowlist return ErrIllegalTransition and leave the caller
// with the input state. This is not a collection of booleans (DESIGN.md §68).
func Step(from State, fromSub Substate, ev Event) (State, Substate, error) {
	to, toSub, ok := allowedStep(from, fromSub, ev)
	if !ok {
		return from, fromSub, &TransitionError{From: from, FromSub: fromSub, Event: ev}
	}
	return to, toSub, nil
}

// allowedStep is the closed allowlist. Unknown events and unlisted
// (from, event) pairs fail closed — they must not map to a destination
// the way the old event-only switch did.
func allowedStep(from State, fromSub Substate, ev Event) (State, Substate, bool) {
	switch ev {
	case EventStartRequested:
		switch from {
		case Inactive, Failed, Activating, Active:
			return Activating, SubStart, true
		}
	case EventStartSucceeded:
		if from == Activating {
			return Active, SubRunning, true
		}
	case EventStartFailed:
		switch from {
		case Activating, Active, Deactivating:
			return Failed, SubNone, true
		}
	case EventStopRequested:
		switch from {
		case Inactive, Active, Activating, Failed, Deactivating:
			return Deactivating, SubStop, true
		}
	case EventStopFinished:
		if from == Deactivating {
			return Inactive, SubNone, true
		}
	case EventMainExited:
		switch from {
		case Active, Activating, Inactive:
			// Inactive covers the watch vs applyRunLocked race: a reaped
			// process is Failed, which matches the live proc.
			return Failed, SubNone, true
		}
	case EventAutoRestart:
		switch from {
		case Inactive, Active, Failed:
			// Inactive: start failed before the manager stamped Failed
			// (Type=simple / Type=scm / Type=scheduled-task); maybeRestart runs in startOne.
			return Activating, SubAutoRestart, true
		}
	case EventRestartCancelled:
		if from == Activating && fromSub == SubAutoRestart {
			return Inactive, SubNone, true
		}
	case EventWatchdogFailed:
		switch from {
		case Active, Activating:
			return Failed, SubWatchdog, true
		}
	}
	return from, fromSub, false
}
