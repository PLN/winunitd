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

func TestOperationCLIQueryAndExitCodes(t *testing.T) {
	for _, tc := range []struct {
		state string
		code  int
	}{{"succeeded", 0}, {"running", 3}, {"failed", 1}, {"missing", 4}, {"old-server", 1}} {
		t.Run(tc.state, func(t *testing.T) {
			const id = "example/op/7"
			dial := func(ctx context.Context) (net.Conn, error) {
				client, server := net.Pipe()
				go func() {
					defer server.Close()
					protocol.ServeConn(ctx, server, protocol.HandlerFunc(func(_ context.Context, method string, raw json.RawMessage) (any, error) {
						var p protocol.OperationParams
						if method != protocol.MethodOperation || protocol.DecodeParams(raw, &p) != nil || p.ID != id {
							return nil, protocol.ErrInvalidParams("unexpected operation query")
						}
						if tc.state == "missing" {
							return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "operation no longer retained"}
						}
						if tc.state == "old-server" {
							return nil, protocol.ErrMethodNotFound(method)
						}
						return &protocol.OperationResult{ID: id, Unit: "work.service", Action: "restart", Origin: "explicit", State: tc.state}, nil
					}), protocol.AllowAdmin)
				}()
				return client, nil
			}
			var out, errb bytes.Buffer
			if code := runCLI([]string{"operation", id}, &out, &errb, dial); code != tc.code {
				t.Fatalf("exit=%d stderr=%s", code, errb.String())
			}
			if tc.state != "missing" && tc.state != "old-server" && !strings.Contains(out.String(), "OperationID="+id) {
				t.Fatal("missing operation identity")
			}
		})
	}
}

func TestOperationFailurePrintsQueryableID(t *testing.T) {
	var out, errb bytes.Buffer
	c := &cli{stdout: &out, stderr: &errb}
	if c.rpcError(&protocol.Error{Code: protocol.CodeFailed, Message: "cleanup failed", OperationID: "example/op/2"}) != 1 || !strings.Contains(errb.String(), "OperationID=example/op/2") {
		t.Fatal("error omitted operation ID")
	}
}
