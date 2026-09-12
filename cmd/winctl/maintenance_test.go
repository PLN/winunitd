package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
)

func TestCLIMaintenanceResultAndOldServer(t *testing.T) {
	for _, state := range []string{"quiesced", "quiescing", "old-server", "failed"} {
		t.Run(state, func(t *testing.T) {
			var out, errb bytes.Buffer
			done := make(chan struct{})
			dial := func(context.Context) (net.Conn, error) {
				server, client := net.Pipe()
				go func() {
					defer close(done)
					defer server.Close()
					protocol.ServeConn(context.Background(), server, protocol.HandlerFunc(func(_ context.Context, method string, params json.RawMessage) (any, error) {
						if method != protocol.MethodMaintenance {
							t.Errorf("method=%s", method)
						}
						var p protocol.MaintenanceParams
						if err := protocol.DecodeParams(params, &p); err != nil || p.TimeoutMS != 250 {
							t.Errorf("params=%s error=%v", params, err)
						}
						if state == "old-server" {
							return nil, protocol.ErrMethodNotFound(method)
						}
						if state == "failed" {
							return nil, protocol.ErrFailed("owned work remains")
						}
						return protocol.MaintenanceResult{State: state}, nil
					}), protocol.AllowAdmin)
				}()
				return client, nil
			}
			code := runCLI([]string{"maintenance", "--timeout", "250ms"}, &out, &errb, dial)
			<-done
			if (code == 0) != (state == "quiesced") {
				t.Fatalf("state=%s code=%d stderr=%s", state, code, errb.String())
			}
			if state == "quiesced" && !strings.Contains(out.String(), "restart the manager") {
				t.Error("missing resume instruction")
			}
			if state != "quiesced" && strings.Contains(out.String(), "quiesced") {
				t.Error("failure printed success")
			}
		})
	}
}

func TestCLIMaintenanceRejectsUserScopeAndInvalidTimeout(t *testing.T) {
	for _, args := range [][]string{
		{"--user", "maintenance"},
		{"maintenance", "--timeout", "0s"},
		{"maintenance", "--timeout", "181s"},
		{"maintenance", "--timeout", "invalid"},
		{"maintenance", "unexpected"},
	} {
		var out, errb bytes.Buffer
		called := false
		dial := func(context.Context) (net.Conn, error) { called = true; return nil, nil }
		if code := runCLIUser(args, &out, &errb, dial, dial); code != 2 || called {
			t.Fatalf("args=%v code=%d dialed=%v", args, code, called)
		}
	}
}
