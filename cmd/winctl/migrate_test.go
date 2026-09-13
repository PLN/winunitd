package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestMigratePreviewAndExclusiveOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.service")
	source := []byte("# original\n[Service]\nExecStart=C:\\Apps\\worker.exe\nCPUWeight=5000\n")
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"migrate", "--file", path}, &out, &errb); code != 0 {
		t.Fatalf("preview exit %d: %s", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("FormatVersion=2")) || !bytes.Contains(out.Bytes(), []byte("WindowsCPUWeight=5")) {
		t.Fatal("missing converted preview", out.String())
	}
	preview := bytes.Clone(out.Bytes())
	readSource := func() {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, source) {
			t.Fatal("source changed", err)
		}
	}
	readSource()
	destination := filepath.Join(dir, "new.service")
	out.Reset()
	errb.Reset()
	if code := run([]string{"migrate", "--file", path, "--output", destination}, &out, &errb); code != 0 {
		t.Fatalf("output exit %d: %s", code, errb.String())
	}
	written, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(written, preview) {
		t.Fatal("output differs from preview", err)
	}
	for _, existing := range []string{path, destination} {
		out.Reset()
		errb.Reset()
		if code := run([]string{"migrate", "--file", path, "--output", existing}, &out, &errb); code == 0 {
			t.Fatal("existing file overwritten")
		}
	}
	readSource()
	written, err = os.ReadFile(destination)
	if err != nil || !bytes.Equal(written, preview) {
		t.Fatal("existing destination changed", err)
	}
}

func TestMigrateInvalidInputCreatesNoOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.service")
	destination := filepath.Join(dir, "new.service")
	if err := os.WriteFile(path, []byte("[Unit]\nFormatVersion=99\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"migrate", "--file", path, "--output", destination}, &out, &errb); code == 0 {
		t.Fatal("invalid input accepted")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("invalid conversion wrote output", err)
	}
}
