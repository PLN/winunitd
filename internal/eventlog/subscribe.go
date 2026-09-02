package eventlog

import (
	"errors"
	"strings"
)

// ErrUnknownChannel is returned when the Event Log channel does not exist.
var ErrUnknownChannel = errors.New("event log channel is unknown")

// ErrSubscribeFailed is returned when EvtSubscribe fails for any other reason.
var ErrSubscribeFailed = errors.New("event log subscribe failed")

// ErrEmptyQuery is returned when EvtSubscribe would otherwise be called with
// Query=NULL or an empty string. That would match every event on the channel.
// .eventlog always filters by EventID; a null query is a subscribe error.
var ErrEmptyQuery = errors.New("event log subscribe query is empty")

// subscribeQuery resolves t and returns the EvtSubscribe XPath. A null or
// empty query is an error (no silent match-all). The unit file does not
// accept XPath; this is EventID= only (Trigger.Query).
func subscribeQuery(t Trigger) (Trigger, string, error) {
	if t.Channel == "" || t.EventID == 0 {
		parsed, err := ParseTrigger(t.Raw)
		if err != nil {
			return Trigger{}, "", err
		}
		t = parsed
	}
	if t.Channel == "" || t.EventID == 0 {
		return Trigger{}, "", ErrEmptyQuery
	}
	q := strings.TrimSpace(t.Query())
	if q == "" {
		return Trigger{}, "", ErrEmptyQuery
	}
	return t, q, nil
}

// Subscription is one channel+EventID push watch. C receives a signal
// on each matching event. Close stops the subscription. Implementations
// must be safe for concurrent Close.
type Subscription interface {
	C() <-chan struct{}
	Close() error
}

// OpenFunc opens a push subscription for one EventLogTrigger=.
type OpenFunc func(Trigger) (Subscription, error)
