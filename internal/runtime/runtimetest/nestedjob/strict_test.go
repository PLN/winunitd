package nestedjob

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeStrictRejectsTrailingData(t *testing.T) {
	var v struct{ A int }
	for _, ok := range []string{`{"A":1}`, "{\"A\":1}\n", " {\"A\":1} \r\n\t"} {
		if err := decodeStrict([]byte(ok), &v); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{`{"A":1}]`, `{"A":1}}`, `{"A":1}{"A":2}`, `{"A":1} 2`, `{"A":1}x`, `{"A":1,"B":2}`, ``, `{"A":`} {
		if err := decodeStrict([]byte(bad), &v); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDecodeMatrixRejectsTrailingDelimiter(t *testing.T) {
	// Copy: appending to the embedded table's slice would modify it.
	table := func(extra string) []byte {
		return append(append([]byte(nil), bytes.TrimSpace(matrixJSON)...), extra...)
	}
	if _, err := DecodeMatrix(table("")); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeMatrix(table("]")); err == nil {
		t.Fatal("matrix with a trailing ] accepted")
	}
	if _, err := DecodeMatrix(table("{}")); err == nil {
		t.Fatal("matrix with a second value accepted")
	}
}

func TestReadResultsRejectsASecondObject(t *testing.T) {
	m, err := CaseMatrix()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	r := passing(t, m.Expand()[0])
	if err := WriteResult(dir, r); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadResults(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ResultFileName(r.Key))
	for _, extra := range []string{`{"key":"x"}`, `]`, `}`} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		bad := t.TempDir()
		if err := os.WriteFile(filepath.Join(bad, ResultFileName(r.Key)), append(data, extra...), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadResults(bad); err == nil {
			t.Errorf("record followed by %q accepted", extra)
		}
	}
}

func TestFinalReportsRejectTruncationAndPartialLines(t *testing.T) {
	ready := readyEvents(ModeJobList)
	truncated := append(append([]Event(nil), ready...), Event{Kind: EventTruncated})
	buf := writeEvents(t, 1, truncated...)
	r, err := DecodeReport(bytes.NewReader(buf.Bytes()), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Ready() || !r.Truncated {
		t.Fatalf("decoded %+v", r)
	}
	if err := CheckTree(r, ModeJobList); err == nil {
		t.Fatal("a READY report followed by truncation was accepted")
	}

	dir := absDir(t)
	gen := filepath.Join(dir, GenerationDir(1))
	if err := os.MkdirAll(gen, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(gen, ReportFile), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	complete := writeEvents(t, 1, ready...).Bytes()
	write(complete)
	if r, err := ReadFinalReport(dir, 1); err != nil || r.Partial {
		t.Fatalf("complete report: %+v %v", r, err)
	}
	write(append(append([]byte(nil), complete...), `{"seq":`...))
	if r, err := ReadReport(dir, 1); err != nil || !r.Partial || !r.Ready() {
		t.Fatalf("live polling of a partial line: %+v %v", r, err)
	}
	if _, err := ReadFinalReport(dir, 1); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("final report with a partial line: %v", err)
	}
	write(writeEvents(t, 1, truncated...).Bytes())
	if _, err := ReadFinalReport(dir, 1); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated final report: %v", err)
	}
	write([]byte(`{"seq":1`))
	if r, err := ReadReport(dir, 1); err != nil || !r.Partial || len(r.Events) != 0 {
		t.Fatalf("lone partial line: %+v %v", r, err)
	}
}

func TestFileErrorsDoNotNameDirectories(t *testing.T) {
	dir := absDir(t)
	err := ReadJSON(filepath.Join(dir, "missing.json"), &struct{}{})
	var pe *fs.PathError
	if err == nil || strings.Contains(err.Error(), dir) || !errors.Is(err, fs.ErrNotExist) || !errors.As(err, &pe) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := ReadReport(dir, 1); err == nil || strings.Contains(err.Error(), dir) || !errors.As(err, &pe) {
		t.Fatalf("missing report: %v", err)
	}
	if err := WriteJSON(filepath.Join(dir, "no", "such.json"), 1); err == nil || strings.Contains(err.Error(), dir) {
		t.Fatalf("unwritable file: %v", err)
	}
	// The admitted run manifest and hashed files follow the same rule, so a
	// record's admission error does not name where the manifest lives.
	if _, err := LoadAdmission(filepath.Join(dir, "private-admission.json")); err == nil || strings.Contains(err.Error(), dir) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing admission: %v", err)
	}
	if _, err := FileSHA256(filepath.Join(dir, "missing.exe")); err == nil || strings.Contains(err.Error(), dir) {
		t.Fatalf("missing hashed file: %v", err)
	}
	t.Setenv(EnvResults, absDir(t))
	t.Setenv(EnvAdmission, filepath.Join(dir, "private-admission.json"))
	rec, err := NewRecord("N01", ModeAssign, IdentitySystem, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Finish(ResultPass); err == nil || strings.Contains(err.Error(), dir) {
		t.Fatalf("record without its admission: %v", err)
	}
	f := &Failure{Op: "CreateProcess", Win32: 5, Message: `access to C:\private\case-1 denied for S-1-5-21-1-2-3-1001`}
	if s := SafeFailure(f); s != "CreateProcess win32 5" {
		t.Fatalf("safe failure %q", s)
	}
	if s := SafeFailure(nil); s != "none" {
		t.Fatalf("safe failure %q", s)
	}
}
