package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestQualificationResultRequiresExactEvidence(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	for _, scenarioName := range []string{"runtime", "admission", "configuration", "operations"} {
		scenario, err := qualificationScenario(scenarioName)
		if err != nil {
			t.Fatal(err)
		}
		for _, mutation := range []string{"valid", "bom", "commit", "fixture", "identity", "build", "time", "missing", "duplicate", "unexpected", "oversized"} {
			t.Run(scenarioName+"/"+mutation, func(t *testing.T) {
				passed := append([]string(nil), scenario.passed...)
				result := map[string]any{
					"schema": 1, "commit": commit, "fixture_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(scenario.script))),
					"identity": "SYSTEM", "build": "26100.1", "completed": "2026-09-01T12:00:00Z", "passed": passed,
				}
				switch mutation {
				case "commit":
					result["commit"] = strings.Repeat("f", 40)
				case "fixture":
					result["fixture_sha256"] = strings.Repeat("f", 64)
				case "identity":
					result["identity"] = "Administrator"
				case "build":
					result["build"] = "unknown"
				case "time":
					result["completed"] = "unknown"
				case "missing":
					result["passed"] = passed[:len(passed)-1]
				case "duplicate":
					passed[0] = passed[1]
				case "unexpected":
					passed[0] = "different-check"
				}
				raw, _ := json.Marshal(result)
				if mutation == "bom" {
					raw = append([]byte{0xef, 0xbb, 0xbf}, raw...)
				}
				if mutation == "oversized" {
					raw = append(raw, []byte(strings.Repeat(" ", 16<<10))...)
				}
				if err := checkResult(raw, commit, scenario); (err == nil) != (mutation == "valid" || mutation == "bom") {
					t.Fatalf("evidence admission for %s: %v", mutation, err)
				}
			})
		}
	}
}

func TestQualificationScenarioAllowlist(t *testing.T) {
	for _, name := range []string{"", "../runtime", "runtime;exit", "Runtime"} {
		if _, err := qualificationScenario(name); err == nil {
			t.Fatal("unrecognized scenario admitted")
		}
	}
}
