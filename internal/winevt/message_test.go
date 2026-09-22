package winevt

import (
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/journal"
)

func TestRenderAndRedactDaemonEvents(t *testing.T) {
	text, ok := Render(IDStartupFailed, "listener unavailable")
	if !ok || text != "winunitd startup failed. listener unavailable\n" {
		t.Fatalf("startup render %q ok=%v", text, ok)
	}
	text, ok = Render(IDOpen, "ignored")
	if !ok || strings.Contains(text, "ignored") || !strings.Contains(text, "daemon opened") {
		t.Fatalf("open render %q", text)
	}
	if got := substitute("100%% full %1", []string{"ok"}); got != "100% full ok" {
		t.Fatalf("percent render %q", got)
	}

	id, kind, insert := Format(journal.DaemonEventView{Code: journal.DaemonEventStartupFailed, Reason: "listen failed"})
	if id != IDStartupFailed || kind != KindError || insert != "listen failed" {
		t.Fatalf("startup format id=%d kind=%d insert=%q", id, kind, insert)
	}
	_, _, insert = Format(journal.DaemonEventView{
		Code:   journal.DaemonEventStartupFailed,
		Reason: "rejected Environment=TOKEN=1",
	})
	if insert != "" {
		t.Fatalf("environment assignment survived: %q", insert)
	}
	_, _, insert = Format(journal.DaemonEventView{
		Code:   journal.DaemonEventLifecycleRejected,
		Unit:   "work.service",
		Reason: "store-uri=example",
	})
	if insert != "" {
		t.Fatalf("store uri survived: %q", insert)
	}
	remaining := -2
	burst := 3
	_, kind, insert = Format(journal.DaemonEventView{
		Code:                journal.DaemonEventStartLimit,
		Unit:                "work.service",
		RestartAttempt:      4,
		StartLimitBurst:     &burst,
		StartLimitRemaining: &remaining,
	})
	if kind != KindWarning || insert != "unit=work.service attempt=4 burst=3 remaining=-2" {
		t.Fatalf("start-limit format kind=%d insert=%q", kind, insert)
	}
	if id, _, _ := Format(journal.DaemonEventView{Code: "parser.dump"}); id != 0 {
		t.Fatal("unknown code was formatted")
	}
}

func TestEmitStartupFailureDropsSecrets(t *testing.T) {
	var gotID uint32
	var gotInsert string
	prev := reportEvent
	reportEvent = func(id uint32, _ Kind, insert string) {
		gotID = id
		gotInsert = insert
	}
	t.Cleanup(func() { reportEvent = prev })
	secret := "pass" + "word=hunter2"
	EmitStartupFailure("open failed " + secret)
	if gotID != IDStartupFailed || gotInsert != "" {
		t.Fatalf("emitted id=%d insert=%q", gotID, gotInsert)
	}
	Emit(journal.DaemonEventView{Code: journal.DaemonEventOpen})
	if gotID != IDOpen || gotInsert != "" {
		t.Fatalf("open emit id=%d insert=%q", gotID, gotInsert)
	}
}
