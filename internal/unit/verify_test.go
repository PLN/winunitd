package unit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyPathFileSizeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounded.target")
	for _, size := range []int{MaxFileBytes, MaxFileBytes + 1} {
		data := "[Unit]\n" + strings.Repeat("#\n", (size-7)/2)
		data += strings.Repeat("#", size-len(data))
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		r := VerifyPath(path)
		if r.HasError() != (size > MaxFileBytes) {
			t.Fatalf("size %d: %+v", size, r.Issues)
		}
	}
}

func TestVerifyPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.service")
	if err := os.WriteFile(ok, []byte(`
[Service]
Type=oneshot
ExecStart=C:\Tools\run-once.exe
WorkingDirectory=C:\Tools
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := VerifyPath(ok)
	if rep.HasError() {
		t.Fatalf("unexpected errors: %v", issueTexts(rep.Errors()))
	}
	if rep.Unit == nil || rep.Unit.Name != "ok.service" {
		t.Fatalf("unit name = %+v", rep.Unit)
	}

	missing := filepath.Join(dir, "nope.service")
	rep = VerifyPath(missing)
	if !rep.HasError() {
		t.Fatal("missing file should fail")
	}

	bad := filepath.Join(dir, "bad.service")
	if err := os.WriteFile(bad, []byte(`
[Service]
ExecStart=relative.exe
Conflicts=other.service
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep = VerifyPath(bad)
	if !rep.HasError() {
		t.Fatal("expected errors")
	}
	errs := strings.Join(issueTexts(rep.Errors()), "\n")
	if !strings.Contains(errs, "absolute path") {
		t.Fatalf("missing relative ExecStart error: %s", errs)
	}
	if !strings.Contains(errs, `unknown directive "Conflicts"`) {
		t.Fatalf("unknown directive should fail verify: %s", errs)
	}
	warns := strings.Join(issueTexts(rep.Warnings()), "\n")
	if !strings.Contains(warns, "WorkingDirectory is omitted") {
		t.Fatalf("missing WorkingDirectory warning: %s", warns)
	}
}

func TestVerifyResourceLimitTable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tests := []struct {
		name    string
		file    string
		src     string
		wantErr string
	}{
		{
			name: "MemoryMax=abc",
			file: "bad-mem.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
MemoryMax=abc
`,
			wantErr: `invalid MemoryMax "abc"`,
		},
		{
			name: "ProcessLimit=-1",
			file: "bad-proc.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
ProcessLimit=-1
`,
			wantErr: `invalid ProcessLimit "-1"`,
		},
		{
			name: "PriorityClass=realtime",
			file: "bad-pri.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
PriorityClass=realtime
`,
			wantErr: `invalid PriorityClass "realtime"`,
		},
		{
			name: "CPUWeight=0",
			file: "bad-weight.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
CPUWeight=0
`,
			wantErr: `invalid CPUWeight "0"`,
		},
		{
			name: "CPUQuota=25",
			file: "bad-quota.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
CPUQuota=25
`,
			wantErr: `invalid CPUQuota "25"`,
		},
		{
			name: "CPUQuota=0%",
			file: "bad-quota-zero.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
CPUQuota=0%
`,
			wantErr: `invalid CPUQuota "0%"`,
		},
		{
			name: "CPUQuota=101%",
			file: "bad-quota-101.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
CPUQuota=101%
`,
			wantErr: `invalid CPUQuota "101%"`,
		},
		{
			name: "CPUQuota=10000%",
			file: "bad-quota-10000.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
CPUQuota=10000%
`,
			wantErr: `invalid CPUQuota "10000%"`,
		},
		{
			name: "CPUWeight+CPUQuota",
			file: "bad-both.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
CPUWeight=50
CPUQuota=25%
`,
			wantErr: "CPUWeight and CPUQuota cannot both be set",
		},
		{
			name: "IoPriority=critical",
			file: "bad-io.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
IoPriority=critical
`,
			wantErr: `invalid IoPriority "critical"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(dir, tt.file)
			if err := os.WriteFile(p, []byte(tt.src), 0o644); err != nil {
				t.Fatal(err)
			}
			rep := VerifyPath(p)
			if !rep.HasError() {
				t.Fatal("expected verify error")
			}
			errs := strings.Join(issueTexts(rep.Errors()), "\n")
			if !strings.Contains(errs, tt.wantErr) {
				t.Fatalf("errors = %s, want %q", errs, tt.wantErr)
			}
		})
	}
}

func TestVerifyTimerFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "nightly.timer")
	if err := os.WriteFile(p, []byte(`
[Timer]
OnCalendar=Mon..Fri 03:00
Persistent=true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := VerifyPath(p)
	if rep.HasError() {
		t.Fatalf("errors: %v", issueTexts(rep.Errors()))
	}
	if rep.Unit.Timer.Unit != "nightly.service" {
		t.Fatalf("implicit unit = %q", rep.Unit.Timer.Unit)
	}
}

func TestVerifyRegistryPair(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	reg := filepath.Join(dir, "foo.registry")
	if err := os.WriteFile(reg, []byte(`
[Registry]
RegistryChanged=HKLM\Software\Example
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := VerifyPath(reg)
	if !rep.HasError() {
		t.Fatal("missing companion must fail")
	}
	errs := strings.Join(issueTexts(rep.Errors()), "\n")
	if !strings.Contains(errs, "missing companion foo.service") {
		t.Fatalf("errors = %s", errs)
	}

	if err := os.WriteFile(filepath.Join(dir, "foo.service"), []byte(`
[Service]
Type=oneshot
ExecStart=C:\Tools\run-once.exe
WorkingDirectory=C:\Tools
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep = VerifyPath(reg)
	if rep.HasError() {
		t.Fatalf("pair should verify: %v", issueTexts(rep.Errors()))
	}
	if rep.Unit.Registry.Unit != "foo.service" {
		t.Fatalf("implicit unit = %q", rep.Unit.Registry.Unit)
	}
}

func TestRegistryScopeIssues(t *testing.T) {
	t.Parallel()
	rep := ParseUnit("sys.registry", `
[Registry]
RegistryChanged=HKCU\Software\Example
`)
	if rep.HasError() {
		t.Fatalf("parse: %v", issueTexts(rep.Errors()))
	}
	got := RegistryScopeIssues(rep.Unit, false)
	if len(got) == 0 {
		t.Fatal("system manager must reject HKCU")
	}
	if RegistryScopeIssues(rep.Unit, true) != nil {
		t.Fatal("user manager must accept HKCU")
	}
	hlm := ParseUnit("sys.registry", `
[Registry]
RegistryChanged=HKLM\Software\Example
`)
	if RegistryScopeIssues(hlm.Unit, false) != nil {
		t.Fatal("system manager must accept HKLM")
	}
}

func TestVerifyEventLogPair(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	evt := filepath.Join(dir, "foo.eventlog")
	if err := os.WriteFile(evt, []byte(`
[EventLog]
EventLogTrigger=Application:EventID=1234
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := VerifyPath(evt)
	if !rep.HasError() {
		t.Fatal("missing companion must fail")
	}
	errs := strings.Join(issueTexts(rep.Errors()), "\n")
	if !strings.Contains(errs, "missing companion foo.service") {
		t.Fatalf("errors = %s", errs)
	}

	if err := os.WriteFile(filepath.Join(dir, "foo.service"), []byte(`
[Service]
Type=oneshot
ExecStart=C:\Tools\run-once.exe
WorkingDirectory=C:\Tools
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep = VerifyPath(evt)
	if rep.HasError() {
		t.Fatalf("pair should verify: %v", issueTexts(rep.Errors()))
	}
	if rep.Unit.EventLog.Unit != "foo.service" {
		t.Fatalf("implicit unit = %q", rep.Unit.EventLog.Unit)
	}
}

func TestEventLogScopeIssues(t *testing.T) {
	t.Parallel()
	sys := ParseUnit("sys.eventlog", `
[EventLog]
EventLogTrigger=System:EventID=1
`)
	if sys.HasError() {
		t.Fatalf("parse: %v", issueTexts(sys.Errors()))
	}
	if EventLogScopeIssues(sys.Unit, false) != nil {
		t.Fatal("system manager must accept System")
	}
	got := EventLogScopeIssues(sys.Unit, true)
	if len(got) == 0 {
		t.Fatal("user manager must reject System")
	}

	sec := ParseUnit("sec.eventlog", `
[EventLog]
EventLogTrigger=Security:EventID=1
`)
	if EventLogScopeIssues(sec.Unit, true) == nil {
		t.Fatal("user manager must reject Security")
	}
	if EventLogScopeIssues(sec.Unit, false) != nil {
		t.Fatal("system manager must accept Security")
	}

	app := ParseUnit("app.eventlog", `
[EventLog]
EventLogTrigger=Application:EventID=1
`)
	if EventLogScopeIssues(app.Unit, true) != nil || EventLogScopeIssues(app.Unit, false) != nil {
		t.Fatal("Application is valid in both managers")
	}
	custom := ParseUnit("custom.eventlog", `
[EventLog]
EventLogTrigger=MyLog:EventID=1
`)
	if EventLogScopeIssues(custom.Unit, true) != nil {
		t.Fatal("user manager must accept custom log names")
	}
}

func TestVerifyPathPair(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pth := filepath.Join(dir, "foo.path")
	if err := os.WriteFile(pth, []byte(`
[Path]
PathChanged=C:\Data\incoming
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := VerifyPath(pth)
	if !rep.HasError() {
		t.Fatal("missing companion must fail")
	}
	errs := strings.Join(issueTexts(rep.Errors()), "\n")
	if !strings.Contains(errs, "missing companion foo.service") {
		t.Fatalf("errors = %s", errs)
	}

	if err := os.WriteFile(filepath.Join(dir, "foo.service"), []byte(`
[Service]
Type=oneshot
ExecStart=C:\Tools\run-once.exe
WorkingDirectory=C:\Tools
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep = VerifyPath(pth)
	if rep.HasError() {
		t.Fatalf("pair should verify: %v", issueTexts(rep.Errors()))
	}
	if rep.Unit.PathWatch.Unit != "foo.service" {
		t.Fatalf("implicit unit = %q", rep.Unit.PathWatch.Unit)
	}

	exists := filepath.Join(dir, "bar.path")
	if err := os.WriteFile(exists, []byte(`
[Path]
PathExists=C:\Data\ready.flag
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bar.service"), []byte(`
[Service]
Type=oneshot
ExecStart=C:\Tools\run-once.exe
WorkingDirectory=C:\Tools
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep = VerifyPath(exists)
	if rep.HasError() {
		t.Fatalf("PathExists pair should verify: %v", issueTexts(rep.Errors()))
	}
	if len(rep.Unit.PathWatch.Exists) != 1 || rep.Unit.PathWatch.Exists[0].Raw != `C:\Data\ready.flag` {
		t.Fatalf("exists = %#v", rep.Unit.PathWatch.Exists)
	}

	rel := filepath.Join(dir, "rel.path")
	if err := os.WriteFile(rel, []byte(`
[Path]
PathExists=ready.flag
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep = VerifyPath(rel)
	if !rep.HasError() {
		t.Fatal("relative PathExists must fail")
	}
	errs = strings.Join(issueTexts(rep.Errors()), "\n")
	if !strings.Contains(errs, "absolute Windows path") {
		t.Fatalf("errors = %s", errs)
	}
}
