package unit

import (
	"os"
	"path/filepath"
	"testing"
)

// Keep the shipped getting-started files usable as the parser evolves. Existing
// parse/exec tests cover the complete grammar; this checks the actual artifacts.
func TestBetaExamples(t *testing.T) {
	for _, name := range []string{"worker.service", "worker.target"} {
		src, err := os.ReadFile(filepath.Join("..", "..", "examples", "beta", name))
		if err != nil {
			t.Fatal(err)
		}
		r := Parse(name, name, src)
		if len(r.Issues) != 0 {
			t.Fatalf("%s: %v", name, r.Issues)
		}
		if name == "worker.service" && (len(r.Unit.Service.ExecStart) != 5 || r.Unit.Service.ExecStart[4] != "while ($true) { Write-Output 'worker is running'; Start-Sleep -Seconds 5 }") {
			t.Fatal("worker command no longer reaches PowerShell as one argument")
		}
	}
}
