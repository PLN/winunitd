//go:build !windows

package nestedjob

import (
	"bytes"
	"strings"
	"testing"
)

func TestMainFailsVisiblyWithoutWindows(t *testing.T) {
	var out, errOut bytes.Buffer
	args := MainConfig{LaunchMode: ModeJobList, CaseDir: absDir(t), Generation: 1, Work: WorkIdle, OnStop: OnStopCooperative}.Args()
	if code := Main(nil, args, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "requires Windows") {
		t.Fatalf("exit %d: %q", code, errOut.String())
	}
}
