package unit

import (
	"strings"
	"testing"
	"time"
)

func TestParseUnits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		file      string
		src       string
		wantErr   []string
		forbidErr []string
		wantWarn  []string
		noWarn    bool
		check     func(t *testing.T, u *Unit)
	}{
		{
			name: "basic simple service",
			file: "hermes.service",
			src: `
[Unit]
Description=Hermes Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=C:\Tools\Hermes\hermes.exe agent
WorkingDirectory=C:\Tools\Hermes
Restart=on-failure
RestartSec=5s
TimeoutStartSec=30s
TimeoutStopSec=20s
Environment=HERMES_PROFILE=default
Environment=LOG_LEVEL=info

[Install]
WantedBy=default.target
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				if u.Kind != KindService {
					t.Fatalf("kind = %s", u.Kind)
				}
				if u.Description != "Hermes Agent" {
					t.Fatalf("description = %q", u.Description)
				}
				if u.Service.Type != TypeSimple {
					t.Fatalf("type = %s", u.Service.Type)
				}
				wantArgv := []string{`C:\Tools\Hermes\hermes.exe`, "agent"}
				if strings.Join(u.Service.ExecStart, "\x00") != strings.Join(wantArgv, "\x00") {
					t.Fatalf("argv = %#v", u.Service.ExecStart)
				}
				if u.Service.WorkingDirectory != `C:\Tools\Hermes` {
					t.Fatalf("wd = %q", u.Service.WorkingDirectory)
				}
				if u.Service.Restart != RestartOnFailure {
					t.Fatalf("restart = %s", u.Service.Restart)
				}
				if !u.Service.RestartSecSet || u.Service.RestartSec != 5*time.Second {
					t.Fatalf("restartsec = %v set=%v", u.Service.RestartSec, u.Service.RestartSecSet)
				}
				if len(u.Service.Environment) != 2 || u.Service.Environment[0].Value != "default" {
					t.Fatalf("env = %#v", u.Service.Environment)
				}
				if len(u.WantedBy) != 1 || u.WantedBy[0] != "default.target" {
					t.Fatalf("wantedby = %#v", u.WantedBy)
				}
			},
		},
		{
			name: "oneshot type is valid",
			file: "setup.service",
			src: `
[Service]
Type=oneshot
ExecStart=C:\Tools\run-once.exe
WorkingDirectory=C:\Tools
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				if u.Service.Type != TypeOneshot {
					t.Fatalf("type = %s", u.Service.Type)
				}
				if u.Service.Restart != RestartNo {
					t.Fatalf("default restart = %s", u.Service.Restart)
				}
			},
		},
		{
			name: "RequiresInteractiveSession yes",
			file: "gui.service",
			src: `
[Unit]
Description=GUI helper
RequiresInteractiveSession=yes
[Service]
Type=simple
ExecStart=C:\Tools\gui.exe
WorkingDirectory=C:\Tools
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				if !u.RequiresInteractiveSession {
					t.Fatal("RequiresInteractiveSession")
				}
			},
		},
		{
			name: "SessionMode is not implemented",
			file: "gui.service",
			src: `
[Unit]
SessionMode=linger
[Service]
ExecStart=C:\Tools\gui.exe
WorkingDirectory=C:\Tools
`,
			wantErr: []string{`unknown directive "SessionMode"`},
		},
		{
			name: "execstartarg",
			file: "foo.service",
			src: `
[Service]
ExecStart=C:\Program Files\Foo\foo.exe
ExecStartArg=--listen
ExecStartArg=127.0.0.1:8080
WorkingDirectory=C:\Program Files\Foo
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				want := []string{`C:\Program Files\Foo\foo.exe`, "--listen", "127.0.0.1:8080"}
				if strings.Join(u.Service.ExecStart, "\x00") != strings.Join(want, "\x00") {
					t.Fatalf("argv = %#v", u.Service.ExecStart)
				}
			},
		},
		{
			name: "json array execstart",
			file: "foo.service",
			src: `
[Service]
ExecStart=["C:\\Program Files\\Foo\\foo.exe", "--listen", "127.0.0.1:8080"]
WorkingDirectory=C:\Program Files\Foo
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				want := []string{`C:\Program Files\Foo\foo.exe`, "--listen", "127.0.0.1:8080"}
				if strings.Join(u.Service.ExecStart, "\x00") != strings.Join(want, "\x00") {
					t.Fatalf("argv = %#v", u.Service.ExecStart)
				}
			},
		},
		{
			name: "space-separated compatibility",
			file: "foo.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe --bar
WorkingDirectory=C:\Tools
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				want := []string{`C:\Tools\foo.exe`, "--bar"}
				if strings.Join(u.Service.ExecStart, "\x00") != strings.Join(want, "\x00") {
					t.Fatalf("argv = %#v", u.Service.ExecStart)
				}
			},
		},
		{
			name: "environment literals no expansion",
			file: "foo.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Environment=FOO=${BAR}
Environment=BAZ=%QUX%
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				if len(u.Service.Environment) != 2 {
					t.Fatalf("env = %#v", u.Service.Environment)
				}
				if u.Service.Environment[0].Value != "${BAR}" {
					t.Fatalf("FOO = %q, expansion is not allowed", u.Service.Environment[0].Value)
				}
				if u.Service.Environment[1].Value != "%QUX%" {
					t.Fatalf("BAZ = %q", u.Service.Environment[1].Value)
				}
			},
		},
		{
			name: "unquoted Environment value with spaces fails",
			file: "log.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Environment=WINUNITD_JOB_PRINT=hello from journal
`,
			wantErr: []string{"invalid Environment assignment"},
		},
		{
			name: "quoted Environment value with spaces",
			file: "log.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Environment="WINUNITD_JOB_PRINT=hello from journal"
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				if len(u.Service.Environment) != 1 || u.Service.Environment[0].Value != "hello from journal" {
					t.Fatalf("env = %#v", u.Service.Environment)
				}
			},
		},
		{
			name: "list accumulation and reset",
			file: "foo.service",
			src: `
[Unit]
Requires=a.service b.service
Requires=
Requires=c.service
Wants=one.service
Wants=two.service

[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				if len(u.Requires) != 1 || u.Requires[0] != "c.service" {
					t.Fatalf("requires = %#v", u.Requires)
				}
				if len(u.Wants) != 2 {
					t.Fatalf("wants = %#v", u.Wants)
				}
			},
		},
		{
			name: "comments and continuation",
			file: "foo.service",
			src: `
# comment
; also a comment
[Unit]
Description=Hello \
world

[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				if u.Description != "Hello world" {
					t.Fatalf("description = %q", u.Description)
				}
			},
		},
		{
			name: "working directory trailing backslash is not continuation",
			file: "foo.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools\
Restart=always
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				if u.Service.WorkingDirectory != `C:\Tools\` {
					t.Fatalf("wd = %q, want %q", u.Service.WorkingDirectory, `C:\Tools\`)
				}
				if u.Service.Restart != RestartAlways {
					t.Fatalf("Restart=always was joined into WorkingDirectory; restart = %s", u.Service.Restart)
				}
			},
		},
		{
			name: "omitted working directory warns",
			file: "foo.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
`,
			wantWarn: []string{"WorkingDirectory is omitted"},
			check: func(t *testing.T, u *Unit) {
				if u.Service.WorkingDirectory != "" {
					t.Fatalf("wd should be empty, got %q", u.Service.WorkingDirectory)
				}
			},
		},
		{
			name: "relative execstart fails",
			file: "foo.service",
			src: `
[Service]
ExecStart=foo.exe
WorkingDirectory=C:\Tools
`,
			wantErr: []string{"absolute path"},
		},
		{
			name: "relative execstart with dot slash fails",
			file: "foo.service",
			src: `
[Service]
ExecStart=.\foo.exe
WorkingDirectory=C:\Tools
`,
			wantErr: []string{"absolute path"},
		},
		{
			name: "unknown directive fails",
			file: "foo.service",
			src: `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
WatchdogSec=60s
`,
			wantErr: []string{`unknown directive "WatchdogSec"`},
		},
		{
			name: "unknown section fails",
			file: "foo.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools

[XdgAutostart]
Enabled=true
`,
			wantErr: []string{"unknown section [XdgAutostart]", `unknown directive "Enabled"`},
		},
		{
			name: "invalid type fails",
			file: "foo.service",
			src: `
[Service]
Type=notify
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
			wantErr: []string{`invalid Type "notify"`},
		},
		{
			name: "invalid restart fails",
			file: "foo.service",
			src: `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=on-watchdog
`,
			wantErr: []string{`invalid Restart "on-watchdog"`},
		},
		{
			name: "timer implicit unit",
			file: "foo.timer",
			src: `
[Timer]
OnCalendar=daily
Persistent=yes
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				if u.Kind != KindTimer {
					t.Fatalf("kind = %s", u.Kind)
				}
				if u.Timer.Unit != "foo.service" {
					t.Fatalf("activated unit = %q, want foo.service", u.Timer.Unit)
				}
				if !u.Timer.Persistent {
					t.Fatal("persistent")
				}
				if len(u.Timer.OnCalendar) != 1 || !strings.EqualFold(u.Timer.OnCalendar[0].Raw, "daily") {
					t.Fatalf("calendar = %#v", u.Timer.OnCalendar)
				}
			},
		},
		{
			name: "timer weekday calendar",
			file: "backup.timer",
			src: `
[Timer]
OnCalendar=Mon..Fri 03:00
OnBootSec=5m
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				if u.Timer.Unit != "backup.service" {
					t.Fatalf("activated = %q", u.Timer.Unit)
				}
				if !u.Timer.OnBootSecSet || u.Timer.OnBootSec != 5*time.Minute {
					t.Fatalf("onboot = %v", u.Timer.OnBootSec)
				}
				if len(u.Timer.OnCalendar) != 1 || u.Timer.OnCalendar[0].Hour != 3 {
					t.Fatalf("calendar = %#v", u.Timer.OnCalendar)
				}
			},
		},
		{
			name: "timer star date",
			file: "tick.timer",
			src: `
[Timer]
OnCalendar=*-*-* 15:04:05
OnStartupSec=30s
OnUnitActiveSec=1h
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				if u.Timer.Unit != "tick.service" {
					t.Fatalf("activated = %q", u.Timer.Unit)
				}
				cal := u.Timer.OnCalendar[0]
				if cal.Hour != 15 || cal.Minute != 4 || cal.Second != 5 {
					t.Fatalf("time = %02d:%02d:%02d", cal.Hour, cal.Minute, cal.Second)
				}
				if u.Timer.OnStartupSec != 30*time.Second || u.Timer.OnUnitActiveSec != time.Hour {
					t.Fatalf("monotonic timers missing")
				}
			},
		},
		{
			name: "timer explicit unit override",
			file: "foo.timer",
			src: `
[Timer]
OnCalendar=daily
Unit=other.service
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				if u.Timer.Unit != "other.service" {
					t.Fatalf("activated = %q", u.Timer.Unit)
				}
			},
		},
		{
			name: "bad calendar fails",
			file: "foo.timer",
			src: `
[Timer]
OnCalendar=whenever
`,
			wantErr:   []string{"invalid OnCalendar"},
			forbidErr: []string{"at least one of"},
			check: func(t *testing.T, u *Unit) {
				if u.Timer == nil || u.Timer.Unit != "foo.service" {
					t.Fatalf("implicit unit = %+v", u.Timer)
				}
			},
		},
		{
			name: "bad timer duration fails",
			file: "foo.timer",
			src: `
[Timer]
OnBootSec=soon
`,
			wantErr:   []string{"invalid OnBootSec"},
			forbidErr: []string{"at least one of"},
		},
		{
			name: "timer missing trigger fails",
			file: "foo.timer",
			src: `
[Timer]
Persistent=no
`,
			wantErr: []string{"at least one of"},
		},
		{
			name: "target unit",
			file: "default.target",
			src: `
[Unit]
Description=Default target
Wants=foo.service

[Install]
WantedBy=multi-user.target
`,
			noWarn: true,
			check: func(t *testing.T, u *Unit) {
				if u.Kind != KindTarget {
					t.Fatalf("kind = %s", u.Kind)
				}
				if u.Service != nil || u.Timer != nil {
					t.Fatal("target should not have service/timer specs")
				}
			},
		},
		{
			name: "timer cannot have service section",
			file: "foo.timer",
			src: `
[Timer]
OnCalendar=daily
[Service]
ExecStart=C:\Tools\foo.exe
`,
			wantErr: []string{"section [Service] is not valid in a timer unit"},
		},
		{
			name:    "unsupported suffix",
			file:    "foo.socket",
			src:     `[Unit]\nDescription=nope\n`,
			wantErr: []string{"unsupported unit type"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rep := ParseUnit(tt.file, tt.src)
			errs := issueTexts(rep.Errors())
			warns := issueTexts(rep.Warnings())
			for _, sub := range tt.wantErr {
				if !containsSub(errs, sub) {
					t.Fatalf("missing error substring %q in %v", sub, errs)
				}
			}
			if len(tt.wantErr) == 0 && rep.HasError() {
				t.Fatalf("unexpected errors: %v", errs)
			}
			for _, sub := range tt.forbidErr {
				if containsSub(errs, sub) {
					t.Fatalf("unexpected error substring %q in %v", sub, errs)
				}
			}
			for _, sub := range tt.wantWarn {
				if !containsSub(warns, sub) {
					t.Fatalf("missing warning substring %q in %v", sub, warns)
				}
			}
			if tt.noWarn && len(warns) != 0 {
				t.Fatalf("unexpected warnings: %v", warns)
			}
			if tt.check != nil && rep.Unit != nil {
				tt.check(t, rep.Unit)
			}
		})
	}
}

func issueTexts(iss []Issue) []string {
	out := make([]string, len(iss))
	for i, x := range iss {
		out[i] = x.Message
	}
	return out
}

func containsSub(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestWorkingDirectoryTrailingBackslash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		src    string
		wantWD string
	}{
		{
			name: "Tools trailing backslash is the path",
			src: `[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools\
Restart=always
`,
			wantWD: `C:\Tools\`,
		},
		{
			name: "drive root trailing backslash",
			src: `[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\
Restart=always
`,
			wantWD: `C:\`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rep := ParseUnit("foo.service", tt.src)
			if rep.HasError() {
				t.Fatalf("errors: %v", issueTexts(rep.Errors()))
			}
			got := rep.Unit.Service.WorkingDirectory
			if got != tt.wantWD {
				t.Fatalf("WorkingDirectory = %q, want %q", got, tt.wantWD)
			}
			if rep.Unit.Service.Restart != RestartAlways {
				t.Fatalf("following line was joined into WorkingDirectory; Restart = %s", rep.Unit.Service.Restart)
			}
		})
	}
}
