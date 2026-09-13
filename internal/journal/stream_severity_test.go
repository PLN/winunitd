package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCapturedStreamsDoNotInventSeverity(t *testing.T) {
	s := testStore(t)
	capture := s.Attach("streams.service", 42, "streams", strings.NewReader("fatal-looking stdout\n"), strings.NewReader("ordinary progress on stderr\n"))
	if !capture.WaitContext(context.Background()) {
		t.Fatal("capture did not complete")
	}
	entries, err := s.Read("streams.service")
	if err != nil || len(entries) != 2 {
		t.Fatal("capture records missing", entries, err)
	}
	streams := map[string]bool{}
	for _, entry := range entries {
		streams[entry.Stream] = true
		if entry.Severity != "" || entry.InvocationID != "streams" || entry.PID != 42 {
			t.Fatalf("stream invented severity or lost origin: %+v", entry)
		}
	}
	if !streams["stdout"] || !streams["stderr"] {
		t.Fatal("stream identity lost", streams)
	}
	s.RecordDiagnostic("streams.service", "streams", "supervisor rejected transition")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err = s.Read("streams.service")
	if err != nil || len(entries) != 3 || entries[2].Stream != "daemon" || entries[2].Severity != SeverityErr {
		t.Fatal("explicit daemon severity lost", entries, err)
	}
}

func TestMixedLegacyAndV4SeverityPreservesRecordedValues(t *testing.T) {
	s := testStore(t)
	legacy := []byte("{\"v\":1,\"unit\":\"mixed.service\",\"stream\":\"stderr\",\"message\":\"v1\"}\n" +
		"{\"v\":2,\"unit\":\"mixed.service\",\"stream\":\"stderr\",\"severity\":\"err\",\"message\":\"v2\"}\n" +
		"{\"v\":3,\"unit\":\"mixed.service\",\"stream\":\"stdout\",\"severity\":\"info\",\"message\":\"v3\",\"partial\":true}\n")
	if err := os.WriteFile(s.path("mixed.service"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	capture := s.Attach("mixed.service", 42, "current", nil, strings.NewReader("v4\n"))
	if !capture.WaitContext(context.Background()) {
		t.Fatal("capture did not complete")
	}
	raw, err := os.ReadFile(s.path("mixed.service"))
	if err != nil || !bytes.HasPrefix(raw, legacy) {
		t.Fatal("legacy bytes rewritten", err)
	}
	var current record
	if err := json.Unmarshal(bytes.TrimSpace(raw[len(legacy):]), &current); err != nil {
		t.Fatal(err)
	}
	if current.V != 4 || current.Stream != "stderr" || current.Severity != "" {
		t.Fatalf("current capture contract: %+v", current)
	}
	entries, err := s.Read("mixed.service")
	if err != nil || len(entries) != 4 {
		t.Fatal("mixed history missing", entries, err)
	}
	for i, want := range []string{"", SeverityErr, SeverityInfo, ""} {
		if entries[i].Severity != want {
			t.Fatalf("record %d severity=%q, want original %q", i, entries[i].Severity, want)
		}
	}
	if !entries[2].Partial {
		t.Fatal("legacy fragment metadata lost")
	}
}
