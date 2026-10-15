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

func TestUnitOfProcessCLI(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
		out  string
	}{
		{"main", []string{"unit-of-process", "4321"}, 0, "PID=4321\nUnit=app.service\nInvocationID=inv-1\nMain=true\nProcessCreatedAt=2026-10-15T12:00:00.1234567Z\nScope=user\nSID=S-1-5-21-1-2-3-1001\n"},
		{"outside", []string{"unit-of-process", "77"}, 4, ""},
		{"no pid", []string{"unit-of-process"}, 2, ""},
		{"bad pid", []string{"unit-of-process", "-3"}, 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dial := func(ctx context.Context) (net.Conn, error) {
				client, server := net.Pipe()
				go func() {
					defer server.Close()
					protocol.ServeConn(ctx, server, protocol.HandlerFunc(func(_ context.Context, method string, raw json.RawMessage) (any, error) {
						var p protocol.UnitOfProcessParams
						if method != protocol.MethodUnitOfProcess || protocol.DecodeParams(raw, &p) != nil {
							return nil, protocol.ErrInvalidParams("unexpected query")
						}
						if p.PID != 4321 {
							return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "process 77 belongs to no unit of this manager"}
						}
						return protocol.UnitOfProcessResult{PID: p.PID, Scope: "user", SID: "S-1-5-21-1-2-3-1001", Unit: "app.service", InvocationID: "inv-1", Main: true, ProcessCreatedAt: "2026-10-15T12:00:00.1234567Z"}, nil
					}), protocol.AllowAdmin)
				}()
				return client, nil
			}
			var out, errb bytes.Buffer
			if code := runCLI(tc.args, &out, &errb, dial); code != tc.code {
				t.Fatalf("exit=%d stderr=%s", code, errb.String())
			}
			if out.String() != tc.out {
				t.Fatalf("stdout=%q, want %q", out.String(), tc.out)
			}
			if tc.code == 4 && !strings.Contains(errb.String(), "belongs to no unit") {
				t.Fatalf("stderr=%q", errb.String())
			}
		})
	}
}
