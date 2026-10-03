//go:build windows

package headless

import (
	"os"
	"path/filepath"
	"testing"
)

// A probe configuration staged after the workload started opens the
// loopback listener at the next flush, without a restart; a missing
// configuration is not an error.
func TestWorkloadOpensLoopbackWhenConfigured(t *testing.T) {
	state := t.TempDir()
	w := &workload{state: state, config: filepath.Join(state, "config.json"), exit: make(chan uint32, 1)}
	t.Cleanup(func() {
		if w.echo != nil {
			_ = w.echo.Close()
		}
	})
	if err := w.tick(); err != nil || w.echo != nil {
		t.Fatalf("tick without a configuration: listener %t, %v", w.echo != nil, err)
	}
	if err := os.WriteFile(w.config, []byte(`{"loopback":"127.0.0.1:0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.tick(); err != nil || w.echo == nil {
		t.Fatalf("tick with a loopback configuration: listener %t, %v", w.echo != nil, err)
	}
	first := w.echo
	if err := w.tick(); err != nil || w.echo != first {
		t.Fatal("the listener was opened twice")
	}
	if err := os.WriteFile(w.config, []byte(`{"loopback":`), 0o600); err != nil {
		t.Fatal(err)
	}
	w.echo = nil
	_ = first.Close()
	if err := w.tick(); err == nil {
		t.Fatal("an unreadable configuration was ignored")
	}
}
