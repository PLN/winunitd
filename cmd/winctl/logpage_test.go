package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/runtime/runtimetest"
)

func TestCLILogsReadsAllPages(t *testing.T) {
	const count = 1200
	line := "page-record:" + strings.Repeat("x", 1024) + "\n"
	_, dial, stop := startTestDaemonUnit(t, "[Service]\nExecStart=C:\\Tools\\foo.exe\nWorkingDirectory=C:\\Tools\n", runtimetest.LauncherOutput(strings.Repeat(line, count), ""))
	defer stop()
	var out, errb bytes.Buffer
	for _, verb := range []string{"start", "stop"} {
		if code := runCLI([]string{verb, "foo"}, &out, &errb, dial); code != 0 {
			t.Fatalf("%s: %s", verb, errb.String())
		}
	}
	out.Reset()
	if code := runCLI([]string{"logs", "foo"}, &out, &errb, dial); code != 0 {
		t.Fatal(errb.String())
	}
	if got := strings.Count(out.String(), "page-record:"); got != count {
		t.Fatalf("read %d records, want %d", got, count)
	}
}
