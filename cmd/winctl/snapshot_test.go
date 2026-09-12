package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
)

func TestSnapshotCommandReturnsConsistentJSON(t *testing.T) {
	m, dial, stop := startTestDaemon(t)
	defer stop()
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runCLI([]string{"snapshot"}, &out, &errOut, dial); code != 0 {
		t.Fatalf("snapshot exit %d: %s", code, errOut.String())
	}
	var snapshot protocol.SnapshotResult
	if err := json.Unmarshal(out.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	var work *protocol.UnitSnapshot
	for i := range snapshot.Units {
		if snapshot.Units[i].Name == "foo.service" {
			work = &snapshot.Units[i]
		}
	}
	if work == nil || len(snapshot.Units) != snapshot.Machine.UnitsLoaded || work.MainPID == 0 || work.ConfigRevision != snapshot.Machine.ConfigRevision || snapshot.CapturedAt == "" {
		t.Fatalf("snapshot: %+v", snapshot)
	}
}
