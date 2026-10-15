package manager

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	goruntime "runtime"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
)

func TestUnitOfProcessRejectsInvalidPID(t *testing.T) {
	m, err := New(Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	for _, raw := range []string{`{}`, `{"pid":0}`, `{"pid":-1}`, `{"pid":4294967296}`} {
		_, err := m.Handle(context.Background(), protocol.MethodUnitOfProcess, json.RawMessage(raw))
		var rpcErr *protocol.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	if goruntime.GOOS == "windows" {
		return
	}
	_, err = m.UnitOfProcess(os.Getpid())
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeFailed {
		t.Fatalf("process query off Windows: %v", err)
	}
}
