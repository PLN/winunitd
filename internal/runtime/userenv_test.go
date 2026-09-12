package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserEnvVars(t *testing.T) {
	t.Parallel()
	info := UserInfo{
		SID:      "S-1-5-21-1-2-3-1001",
		Username: "alice",
		Domain:   "TEST",
		Profile:  filepath.Join("C:", "Users", "alice"),
	}
	got := UserEnvVars(info)
	want := map[string]string{
		"USERPROFILE":  info.Profile,
		"LOCALAPPDATA": filepath.Join(info.Profile, "AppData", "Local"),
		"APPDATA":      filepath.Join(info.Profile, "AppData", "Roaming"),
		"TEMP":         filepath.Join(info.Profile, "AppData", "Local", "Temp"),
		"TMP":          filepath.Join(info.Profile, "AppData", "Local", "Temp"),
		"USERNAME":     "alice",
		"USERDOMAIN":   "TEST",
	}
	if len(got) != len(UserEnvKeys) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(UserEnvKeys), got)
	}
	seen := map[string]string{}
	for _, e := range got {
		name, val, ok := strings.Cut(e, "=")
		if !ok {
			t.Fatalf("bad entry %q", e)
		}
		seen[name] = val
	}
	for k, v := range want {
		if seen[k] != v {
			t.Errorf("%s = %q, want %q", k, seen[k], v)
		}
	}
}

func TestUserEnvVarsPreservesResolvedFolders(t *testing.T) {
	info := UserInfo{Profile: "profile", LocalAppData: "redirected-local", RoamingAppData: "redirected-roaming"}
	vars := map[string]string{}
	for _, entry := range UserEnvVars(info) {
		key, value, _ := strings.Cut(entry, "=")
		vars[key] = value
	}
	if vars["LOCALAPPDATA"] != info.LocalAppData || vars["APPDATA"] != info.RoamingAppData || vars["TEMP"] != filepath.Join(info.LocalAppData, "Temp") {
		t.Fatal("resolved known folders replaced with synthesized profile paths")
	}
}

func TestMergeDeterministicUserEnvDropsInteractiveDump(t *testing.T) {
	t.Parallel()
	info := UserInfo{
		Username: "alice",
		Domain:   "TEST",
		Profile:  filepath.Join("C:", "Users", "alice"),
	}
	parent := []string{
		"SystemRoot=C:\\Windows",
		"Path=C:\\Windows\\System32",
		"USERNAME=SYSTEM",
		"USERPROFILE=C:\\Windows\\system32\\config\\systemprofile",
		"LOCALAPPDATA=C:\\Windows\\system32\\config\\systemprofile\\AppData\\Local",
		"SESSIONNAME=Console",
		"CLIENTNAME=PC",
		"HOMEDRIVE=C:",
		"HOMEPATH=\\Users\\alice",
		"LOGONSERVER=\\\\DC",
	}
	got := MergeDeterministicUserEnv(parent, info)
	m := map[string]string{}
	for _, e := range got {
		name, val, ok := strings.Cut(e, "=")
		if !ok {
			t.Fatalf("bad entry %q", e)
		}
		m[name] = val
	}
	if m["SystemRoot"] != "C:\\Windows" || m["Path"] != "C:\\Windows\\System32" {
		t.Fatalf("machine vars lost: %v", m)
	}
	if m["USERNAME"] != "alice" {
		t.Fatalf("USERNAME = %q", m["USERNAME"])
	}
	if m["USERPROFILE"] != info.Profile {
		t.Fatalf("USERPROFILE = %q", m["USERPROFILE"])
	}
	for _, k := range []string{"SESSIONNAME", "CLIENTNAME", "HOMEDRIVE", "HOMEPATH", "LOGONSERVER"} {
		if _, ok := m[k]; ok {
			t.Errorf("interactive dump key %s leaked: %q", k, m[k])
		}
	}
}

func TestApplyUserEnv(t *testing.T) {
	info := UserInfo{
		Username: "applyuser",
		Domain:   "APPLYDOM",
		Profile:  t.TempDir(),
	}
	t.Setenv("SESSIONNAME", "Console")
	t.Setenv("USERNAME", "old")
	if err := ApplyUserEnv(info); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("USERNAME") != "applyuser" {
		t.Fatalf("USERNAME = %q", os.Getenv("USERNAME"))
	}
	if os.Getenv("USERDOMAIN") != "APPLYDOM" {
		t.Fatalf("USERDOMAIN = %q", os.Getenv("USERDOMAIN"))
	}
	if os.Getenv("USERPROFILE") != info.Profile {
		t.Fatalf("USERPROFILE = %q", os.Getenv("USERPROFILE"))
	}
	if os.Getenv("SESSIONNAME") != "" {
		t.Fatalf("SESSIONNAME should be cleared, got %q", os.Getenv("SESSIONNAME"))
	}
}
