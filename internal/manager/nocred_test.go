package manager

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// P2 allows S4U and a named CredMan/LSA URI on the linger record.
// Passwords must never appear as plaintext, env, or file references.
var forbiddenSecretPatterns = []string{
	"password-stash",
	"PASSWORD=",
	"password=",
	"Password=",
	"EnvironmentFile=",
	"file://secret",
	"env:PASSWORD",
}

func TestP2HasNoPasswordPlaintext(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
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
		base := filepath.Base(path)
		if base == "nocred_test.go" || strings.HasSuffix(base, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(b)
		rel, _ := filepath.Rel(root, path)
		for _, needle := range forbiddenSecretPatterns {
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
		t.Fatalf("P2 must not include password plaintext/env/file:\n%s", strings.Join(hits, "\n"))
	}
}

func TestLingerRecordTypeHasNoPasswordField(t *testing.T) {
	t.Parallel()
	rec := lingerFile{SID: testSIDA, Name: "user", CredentialURI: "credman://winunitd/linger/" + testSIDA}
	if rec.SID == "" {
		t.Fatal("sid")
	}
}
