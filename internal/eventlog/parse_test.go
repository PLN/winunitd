package eventlog

import (
	"strings"
	"testing"
)

func TestParseTrigger(t *testing.T) {
	t.Parallel()
	ok, err := ParseTrigger("System:EventID=1234")
	if err != nil {
		t.Fatal(err)
	}
	if ok.Channel != "System" || ok.EventID != 1234 {
		t.Fatalf("got %+v", ok)
	}

	app, err := ParseTrigger("  Application:EventID=42  ")
	if err != nil {
		t.Fatal(err)
	}
	if app.Channel != "Application" || app.EventID != 42 {
		t.Fatalf("got %+v", app)
	}

	custom, err := ParseTrigger(`Microsoft-Windows-WindowsUpdateClient/Operational:EventID=19`)
	if err != nil {
		t.Fatal(err)
	}
	if custom.Channel != "Microsoft-Windows-WindowsUpdateClient/Operational" || custom.EventID != 19 {
		t.Fatalf("got %+v", custom)
	}

	max, err := ParseTrigger("Application:EventID=65535")
	if err != nil {
		t.Fatal(err)
	}
	if max.EventID != 65535 {
		t.Fatalf("got %+v", max)
	}

	tests := []struct {
		raw     string
		wantErr string
	}{
		{"", "empty EventLogTrigger"},
		{"System:", "<Channel>:EventID=<uint16>"},
		{"EventID=abc", "<Channel>:EventID=<uint16>"},
		{"System:EventID=abc", "EventID must be a number"},
		{"System:EventID=", "EventID must be a number"},
		{"System:EventID=0", "EventID=0"},
		{":EventID=1", "empty channel"},
		{"System:EventID=65536", "EventID must be a number"},
		{"System:EventID=-1", "EventID must be a number"},
		{"System:EventID=1.5", "EventID must be a number"},
		{"System:EventID=0x10", "EventID must be a number"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			t.Parallel()
			_, err := ParseTrigger(tt.raw)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestTriggerRestrictedInUserScope(t *testing.T) {
	t.Parallel()
	sys, err := ParseTrigger("System:EventID=1")
	if err != nil {
		t.Fatal(err)
	}
	sec, err := ParseTrigger("Security:EventID=1")
	if err != nil {
		t.Fatal(err)
	}
	app, err := ParseTrigger("Application:EventID=1")
	if err != nil {
		t.Fatal(err)
	}
	custom, err := ParseTrigger("MyLog:EventID=1")
	if err != nil {
		t.Fatal(err)
	}
	lower, err := ParseTrigger("system:EventID=1")
	if err != nil {
		t.Fatal(err)
	}
	if !sys.RestrictedInUserScope() || !sec.RestrictedInUserScope() || !lower.RestrictedInUserScope() {
		t.Fatal("System and Security are restricted in a user manager")
	}
	if app.RestrictedInUserScope() || custom.RestrictedInUserScope() {
		t.Fatal("Application and custom names are allowed in a user manager")
	}
}

func TestEventIDFromXML(t *testing.T) {
	t.Parallel()
	id, ok := eventIDFromXML(`<Event><System><EventID>1234</EventID></System></Event>`)
	if !ok || id != 1234 {
		t.Fatalf("got %d %v", id, ok)
	}
	classic, ok := eventIDFromXML(`<Event><System><EventID Qualifiers="16384">40001</EventID></System></Event>`)
	if !ok || classic != 40001 {
		t.Fatalf("classic = %d %v", classic, ok)
	}
	if _, ok := eventIDFromXML(`<Event><System></System></Event>`); ok {
		t.Fatal("missing EventID must fail")
	}
	if _, ok := eventIDFromXML(`<Event><System><EventID>0</EventID></System></Event>`); ok {
		t.Fatal("EventID=0 must fail")
	}
}

func TestTriggerQuery(t *testing.T) {
	t.Parallel()
	tr, err := ParseTrigger("Application:EventID=4242")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Query() != "*[System[(EventID=4242)]]" {
		t.Fatalf("query = %q", tr.Query())
	}
}
