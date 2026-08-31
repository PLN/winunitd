package manager

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Forbidden in P1: passwords, CredMan, LSA, S4U, EnvironmentFile secrets.
var forbiddenCredAPIs = []string{
	"CredRead",
	"CredWrite",
	"CredMan",
	"LogonUser",
	"LsaLogonUser",
	"LsaConnectUntrusted",
	"KERB_S4U",
	"S4ULogon",
	"LoadCredential",
	"password-stash",
	"credman://",
}

func TestP1HasNoPasswordOrCredentialStore(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// manager tests run in internal/manager; scan the module root.
	root = filepath.Clean(filepath.Join(root, "..", ".."))
	var hits []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "nocred_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(b)
		rel, _ := filepath.Rel(root, path)
		for _, needle := range forbiddenCredAPIs {
			if strings.Contains(text, needle) {
				hits = append(hits, rel+": "+needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Fatalf("P1 must not include password/cred-store code:\n%s", strings.Join(hits, "\n"))
	}
}
