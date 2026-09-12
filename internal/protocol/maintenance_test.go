package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestMaintenanceRequiresAdministrativePeer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		peer    Peer
		allowed bool
	}{
		{"owner", Peer{Owner: true}, false},
		{"admin", Peer{Administrator: true}, true},
		{"system", Peer{LocalSystem: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
				called = true
				return MaintenanceResult{State: "quiesced"}, nil
			})
			r := dispatch(context.Background(), h, tc.peer, &Request{Protocol: Name, Version: Version, Method: MethodMaintenance})
			if called != tc.allowed || (r.Error == nil) != tc.allowed {
				t.Fatalf("authorization: called=%v error=%v", called, r.Error)
			}
		})
	}
}

func TestMaintenanceUsesReservedStopAdmission(t *testing.T) {
	called := false
	h := &admittedHandler{next: HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) { called = true; return nil, nil }), requests: make(chan struct{}, 1), stops: make(chan struct{}, 1), diagnostics: make(chan struct{}, 1)}
	h.requests <- struct{}{}
	h.diagnostics <- struct{}{}
	if _, err := h.Handle(context.Background(), MethodMaintenance, nil); err != nil || !called {
		t.Fatal("maintenance did not use reserved stop capacity", err)
	}
	h.stops <- struct{}{}
	called = false
	_, err := h.Handle(context.Background(), MethodMaintenance, nil)
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != CodeBusy || called {
		t.Fatal("maintenance bypassed stop bound")
	}
}
