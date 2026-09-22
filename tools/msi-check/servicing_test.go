package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestServiceStopExceedsStockMSIWait(t *testing.T) {
	if msiServiceControlWait != 30*time.Second {
		t.Fatalf("stock MSI wait = %s", msiServiceControlWait)
	}
	if serviceStopBudget != 180*time.Second || serviceStopBudget <= msiServiceControlWait {
		t.Fatalf("stop budget %s does not exceed the stock MSI wait", serviceStopBudget)
	}
	if quiesceBudget != 180*time.Second {
		t.Fatalf("quiesce budget = %s", quiesceBudget)
	}
}

func TestPlanServiceStop(t *testing.T) {
	quiesce, err := planServiceStop("running")
	if err != nil || !quiesce {
		t.Fatalf("running: quiesce=%v err=%v", quiesce, err)
	}
	quiesce, err = planServiceStop("stopped")
	if err != nil || quiesce {
		t.Fatalf("stopped: quiesce=%v err=%v", quiesce, err)
	}
	if _, err := planServiceStop("transition"); err == nil || !strings.Contains(err.Error(), "abort replacement") {
		t.Fatalf("transition error: %v", err)
	}
}

func TestRequireQuiesced(t *testing.T) {
	if err := requireQuiesced("quiesced", nil); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"", "quiescing", "failed"} {
		if err := requireQuiesced(state, nil); err == nil || !strings.Contains(err.Error(), "abort replacement") {
			t.Fatalf("state %q: %v", state, err)
		}
	}
	if err := requireQuiesced("quiesced", errors.New("closed")); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("call error: %v", err)
	}
}

func TestReplacementBlocked(t *testing.T) {
	if err := replacementBlocked(false, nil); err != nil {
		t.Fatal(err)
	}
	if err := replacementBlocked(true, nil); err == nil || !strings.Contains(err.Error(), "service process is still running") {
		t.Fatalf("process: %v", err)
	}
	err := replacementBlocked(false, []string{"winctl.exe", "winunitd.exe"})
	if err == nil || !strings.Contains(err.Error(), "winctl.exe") || !strings.Contains(err.Error(), "abort replacement") {
		t.Fatalf("handles: %v", err)
	}
}

func TestServiceToken(t *testing.T) {
	if !validServiceToken("0123456789abcdef0123456789abcdef") {
		t.Fatal("expected lowercase hex token")
	}
	for _, token := range []string{"", "../state", "0123456789abcdef0123456789ABCDEF", "short"} {
		if validServiceToken(token) {
			t.Fatalf("accepted %q", token)
		}
	}
}
