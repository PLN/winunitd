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
	for _, scenario := range []string{"valid", "valid-v2", "missing-notice", "missing-source-hash", "dirty", "wrong-commit", "changed-bytes", "path-traversal", "missing-artifact", "duplicate"} {
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

			if scenario == "valid-v2" || scenario == "missing-notice" || scenario == "missing-source-hash" {
				m["schema"] = 2
				m["third_party_sha256"] = fmt.Sprintf("%x", sha256.Sum256([]byte("source fixture")))
				if scenario == "missing-source-hash" {
					delete(m, "third_party_sha256")
				}
				if scenario != "missing-notice" {
					data := []byte("license fixture")
					if err := os.WriteFile(filepath.Join(dir, "THIRD-PARTY-NOTICES.txt"), data, 0600); err != nil {
						t.Fatal(err)
					}
					artifacts = append(artifacts, map[string]any{"name": "THIRD-PARTY-NOTICES.txt", "size": len(data), "sha256": fmt.Sprintf("%x", sha256.Sum256(data))})
				}
			}
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
			if (err == nil) != (scenario == "valid" || scenario == "valid-v2") {
				t.Fatalf("admission outcome: %v", err)
			}
			if scenario == "valid" || scenario == "valid-v2" {
				again, err := smokeArchive(dir, commit)
				if err != nil || !bytes.Equal(archive, again) {
					t.Fatal("archive identity not reproducible")
				}
			}
		})
	}
}
