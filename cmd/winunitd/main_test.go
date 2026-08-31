package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunHelp(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--help"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), `\\.\pipe\winunitd\control`) {
		t.Fatalf("help missing pipe: %s", out.String())
	}
}

func TestRunUnexpectedArg(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"serve"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
}
