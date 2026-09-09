package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ServerLimits bounds transport work separately from manager operation budgets.
// Zero fields select defaults. Stop and diagnostic handlers never borrow normal
// request slots; handler completion does not acquire another admission slot.
type ServerLimits struct {
	Connections  int
	Requests     int
	Stops        int
	Diagnostics  int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

func (l ServerLimits) defaults() (ServerLimits, error) {
	if l.Connections < 0 || l.Requests < 0 || l.Stops < 0 || l.Diagnostics < 0 || l.ReadTimeout < 0 || l.WriteTimeout < 0 {
		return l, fmt.Errorf("server limits must not be negative")
	}
	if l.Connections == 0 {
		l.Connections = 128
	}
	if l.Requests == 0 {
		l.Requests = 64
	}
	if l.Stops == 0 {
		l.Stops = 8
	}
	if l.Diagnostics == 0 {
		l.Diagnostics = 8
	}
	if l.ReadTimeout == 0 {
		l.ReadTimeout = 30 * time.Second
	}
	if l.WriteTimeout == 0 {
		l.WriteTimeout = 5 * time.Second
	}
	// Leave room to decode/reject new work while every handler class is full.
	if l.Requests >= l.Connections || l.Stops >= l.Connections-l.Requests || l.Diagnostics >= l.Connections-l.Requests-l.Stops {
		return l, fmt.Errorf("connection limit must exceed total handler limits")
	}
	return l, nil
}

type admittedHandler struct {
	next        Handler
	requests    chan struct{}
	stops       chan struct{}
	diagnostics chan struct{}
}

func (h *admittedHandler) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	slots := h.requests
	switch method {
	case MethodStop, MethodDisableLinger:
		slots = h.stops
	case MethodStatus, MethodOperation, MethodListUnits, MethodListTimers:
		slots = h.diagnostics
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		return nil, ErrBusy()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return h.next.Handle(ctx, method, params)
}
