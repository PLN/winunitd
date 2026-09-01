package eventlog

import "errors"

// ErrUnknownChannel is returned when the Event Log channel does not exist.
var ErrUnknownChannel = errors.New("event log channel is unknown")

// ErrSubscribeFailed is returned when EvtSubscribe fails for any other reason.
var ErrSubscribeFailed = errors.New("event log subscribe failed")

// Subscription is one channel+EventID push watch. C receives a signal
// on each matching event. Close stops the subscription. Implementations
// must be safe for concurrent Close.
type Subscription interface {
	C() <-chan struct{}
	Close() error
}

// OpenFunc opens a push subscription for one EventLogTrigger=.
type OpenFunc func(Trigger) (Subscription, error)
