package nestedjob

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testSource = "0123456789abcdef0123456789abcdef01234567"

// testFixtureHash stands for the admitted standalone fixture image.
var testFixtureHash = strings.Repeat("ab", 32)

var testAdmission = sync.OnceValues(func() ([]byte, error) {
	exe, err := ExecutableSHA256()
	if err != nil {
		return nil, err
	}
	return json.Marshal(Admission{Schema: AdmissionSchema, Source: testSource, Artifacts: []Artifact{
		{Name: "nestedjob.test", SHA256: exe},
		{Name: "nested-job.exe", SHA256: testFixtureHash},
	}})
})

// testRun is an admitted run whose manifest admits this test binary.
func testRun(t *testing.T) AdmittedRun {
	t.Helper()
	data, err := testAdmission()
	if err != nil {
		t.Fatal(err)
	}
	run, err := DecodeAdmission(data)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// writeTestAdmission writes the test manifest and returns its path.
func writeTestAdmission(t *testing.T) string {
	t.Helper()
	data, err := testAdmission()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(absDir(t), "admission.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDecodeAdmission(t *testing.T) {
	run := testRun(t)
	if run.Manifest.Source != testSource || len(run.Hash) != 64 || !run.Manifest.Admits(testFixtureHash) || run.Manifest.Admits(strings.Repeat("cd", 32)) {
		t.Fatalf("run %+v", run)
	}
	if art, ok := run.Manifest.Lookup("nested-job.exe"); !ok || art.SHA256 != testFixtureHash {
		t.Fatalf("lookup %+v", art)
	}
	var nilManifest *Admission
	if nilManifest.Admits(testFixtureHash) {
		t.Fatal("a missing manifest admitted an artifact")
	}
	good := `{"schema":1,"source":"` + testSource + `","dirty":false,"artifacts":[{"name":"a.exe","sha256":"` + testFixtureHash + `"}]}`
	if _, err := DecodeAdmission([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"schema":         strings.Replace(good, `"schema":1`, `"schema":2`, 1),
		"short source":   strings.Replace(good, testSource, testSource[:12], 1),
		"dirty":          strings.Replace(good, `"dirty":false`, `"dirty":true`, 1),
		"no artifacts":   `{"schema":1,"source":"` + testSource + `","dirty":false,"artifacts":[]}`,
		"bad hash":       strings.Replace(good, testFixtureHash, "ABAB", 1),
		"duplicate name": strings.Replace(good, `}]}`, `},{"name":"a.exe","sha256":"`+testFixtureHash+`"}]}`, 1),
		"bad name":       strings.Replace(good, `a.exe`, `..\a.exe`, 1),
		"unknown field":  strings.Replace(good, `"dirty"`, `"extra":1,"dirty"`, 1),
		"second value":   good + `{}`,
		"trailing ]":     good + `]`,
	} {
		if _, err := DecodeAdmission([]byte(data)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
