package headless

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func observeArgs(t *testing.T, extra ...string) []string {
	t.Helper()
	dir := t.TempDir()
	return append([]string{"--report", filepath.Join(dir, "observer.json"), "--admission", filepath.Join(dir, "admission.json"),
		"--daemon-image", filepath.Join(dir, "winunitd.exe"), "--workload-image", filepath.Join(dir, "headless-workload.exe"),
		"--account", "A=" + sidA, "--account", "B=" + sidB, "--duration", "10m"}, extra...)
}

func TestParseObserve(t *testing.T) {
	c, err := parseObserve(observeArgs(t, "--target", "A", "--crash", "manager", "--crash-lives", "5s,5s,130s", "--crash-rest", "5s"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Scan != defaultScan || len(c.Accounts) != 2 || c.Accounts[AccountB] != sidB {
		t.Fatalf("config %+v", c)
	}
	for i, want := range []time.Duration{5 * time.Second, 5 * time.Second, 130 * time.Second, 5 * time.Second, 5 * time.Second} {
		if life, crash := c.LifeOf(i); !crash || life != want {
			t.Errorf("generation %d life %v %t", i, life, crash)
		}
	}
	if p := c.Plan(); p.Crash != RoleManager || p.Target != AccountA || strings.Join(p.Lives, ",") != "5s,5s,2m10s" || p.Rest != "5s" {
		t.Fatalf("plan %+v", p)
	}
	once, err := parseObserve(observeArgs(t, "--crash", "broker", "--crash-lives", "5s"))
	if err != nil {
		t.Fatal(err)
	}
	if _, crash := once.LifeOf(1); crash {
		t.Fatal("a generation beyond the lives crashed without a rest")
	}
	if c, err := parseObserve(observeArgs(t, "--target", "B", "--crash", "manager", "--crash-lives", "5s", "--crash-class", "wts")); err != nil || c.CrashClass != SourceWTS {
		t.Fatalf("crash class: %v", err)
	}
	if _, err := parseObserve(observeArgs(t, "--target", "B", "--release-file", filepath.Join(t.TempDir(), "release"), "--release-after", "10")); err != nil {
		t.Fatalf("release: %v", err)
	}
	for name, extra := range map[string][]string{
		"relative report":        {"--report", "observer.json"},
		"no duration":            {"--duration", "0s"},
		"long duration":          {"--duration", "4h"},
		"slow scan":              {"--scan", "200ms"},
		"fast scan":              {"--scan", "1ms"},
		"SYSTEM account":         {"--account", "A=" + SystemSID},
		"unknown account role":   {"--account", "C=" + sidAdmin},
		"account twice":          {"--account", "A=" + sidAdmin},
		"target unwatched":       {"--target", "C"},
		"crash without target":   {"--crash", "workload", "--crash-lives", "5s"},
		"crash child":            {"--target", "A", "--crash", "child", "--crash-lives", "5s"},
		"crash without lives":    {"--crash", "broker"},
		"lives without crash":    {"--crash-lives", "5s"},
		"negative life":          {"--crash", "broker", "--crash-lives", "-5s"},
		"bad life":               {"--crash", "broker", "--crash-lives", "soon"},
		"release without file":   {"--target", "A", "--release-after", "3"},
		"release without target": {"--release-file", filepath.Join(t.TempDir(), "r"), "--release-after", "3"},
		"relative marks":         {"--marks", "marks"},
		"crash class without plan": {"--crash-class", "wts"},
		"unknown crash class":      {"--target", "B", "--crash", "manager", "--crash-lives", "5s", "--crash-class", "system"},
		"broker crash class":       {"--crash", "broker", "--crash-lives", "5s", "--crash-class", "s4u"},
		"extra argument":         {"extra"},
	} {
		if _, err := parseObserve(observeArgs(t, extra...)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	shared := observeArgs(t)
	for i, a := range shared {
		if a == "B="+sidB {
			shared[i] = "B=" + sidA
		}
	}
	if _, err := parseObserve(shared); err == nil || !strings.Contains(err.Error(), "both accounts have one SID") {
		t.Errorf("two accounts with one SID: %v", err)
	}
}

// Usage errors name the flag, never the identities passed to it.
func TestObserveDiagnosticsOmitIdentities(t *testing.T) {
	const secret = "S-1-5-21-424242-1-2-1001"
	for _, extra := range [][]string{{"--account", "A=" + secret}, {"--account", "C=" + secret}, {"--account", "A=" + SystemSID}} {
		_, err := parseObserve(append(observeArgs(t), extra...))
		if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), sidA) || strings.Contains(err.Error(), sidB) {
			t.Errorf("%v: diagnostic names an identity: %t", extra[1][:2], err != nil)
		}
	}
}
