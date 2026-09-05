package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactAdmission(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	for _, scenario := range []string{"valid", "dirty", "wrong-commit", "changed-bytes", "path-traversal", "missing-artifact", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			artifacts := []map[string]any{}
			for _, name := range []string{"winunitd.exe", "winctl.exe", "winunit-notify.exe"} {
				data := []byte("fixture " + name)
				if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
				artifacts = append(artifacts, map[string]any{"name": name, "size": len(data), "sha256": fmt.Sprintf("%x", sha256.Sum256(data))})
			}
			m := map[string]any{"schema": 1, "commit": commit, "dirty": false, "goos": "windows", "goarch": "amd64"}
			switch scenario {
			case "dirty":
				m["dirty"] = true
			case "wrong-commit":
				m["commit"] = "fedcba9876543210fedcba9876543210fedcba98"
			case "changed-bytes":
				if err := os.WriteFile(filepath.Join(dir, "winctl.exe"), []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			case "path-traversal":
				artifacts[0]["name"] = "../winunitd.exe"
			case "missing-artifact":
				artifacts = artifacts[:2]
			case "duplicate":
				artifacts = append(artifacts, artifacts[0])
			}
			m["artifacts"] = artifacts
			raw, _ := json.Marshal(m)
			if err := os.WriteFile(filepath.Join(dir, "build-manifest.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			archive, err := smokeArchive(dir, commit)
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("admission outcome: %v", err)
			}
			if scenario == "valid" {
				again, err := smokeArchive(dir, commit)
				if err != nil || !bytes.Equal(archive, again) {
					t.Fatal("archive identity not reproducible")
				}
			}
		})
	}
}
