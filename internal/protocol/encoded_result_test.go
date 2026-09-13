package protocol

import (
	"context"
	"encoding/json"
	"io"
	"sync/atomic"
	"testing"
)

type countedSnapshot struct {
	value SnapshotResult
	calls *atomic.Uint64
}

func (s countedSnapshot) MarshalJSON() ([]byte, error) {
	s.calls.Add(1)
	return json.Marshal(s.value)
}

func TestEncodedSnapshotIsSerializedOncePerRequest(t *testing.T) {
	var calls atomic.Uint64
	h := HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
		return EncodeResult(countedSnapshot{value: SnapshotResult{ManagerID: "example", Sequence: 7}, calls: &calls})
	})
	client, stop := serveTest(t, h, AllowAdmin)
	defer stop()
	got, err := client.Snapshot(context.Background())
	if err != nil || got.ManagerID != "example" || got.Sequence != 7 {
		t.Fatalf("snapshot reply: %+v %v", got, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("snapshot result encoded %d times", calls.Load())
	}
}

func TestUninitializedEncodedResultIsAnError(t *testing.T) {
	got := newResponse(1, EncodedResult{}, nil)
	if got.Error == nil || got.Result != nil {
		t.Fatal("invalid encoded result returned success")
	}
}

func BenchmarkEncodedSnapshotResponse(b *testing.B) {
	value := SnapshotResult{ManagerID: "example", Sequence: 7, Units: make([]UnitSnapshot, 1024)}
	for i := range value.Units {
		value.Units[i] = UnitSnapshot{Name: "work.service", ActiveState: "active", InvocationID: "example-invocation"}
	}
	for _, reuse := range []bool{false, true} {
		name := "reencode"
		if reuse {
			name = "reuse"
		}
		b.Run(name, func(b *testing.B) {
			var calls atomic.Uint64
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				counted := countedSnapshot{value: value, calls: &calls}
				encoded, err := EncodeResult(counted)
				if err != nil {
					b.Fatal(err)
				}
				var result any = counted
				if reuse {
					result = encoded
				}
				if err := encodeMessage(io.Discard, newResponse(1, result, nil)); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(calls.Load())/float64(b.N), "result-encodes/op")
		})
	}
}
