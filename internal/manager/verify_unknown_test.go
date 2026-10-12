package manager

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/unit"
)

// The daemon applies the parser's error policy: reload rejects a candidate
// with an unknown directive and keeps the accepted revision, and verify of a
// loaded unit whose file gained one reports an error, not a warning.
func TestDaemonRejectsUnknownDirectivesAsErrors(t *testing.T) {
	t.Parallel()
	const valid = "[Service]\nExecStart=C:\\Tools\\foo.exe\nWorkingDirectory=C:\\Tools\n"
	m := testManager(t, map[string]string{"foo.service": valid})
	before, err := m.Status("")
	if err != nil {
		t.Fatal(err)
	}
	units := m.cfg.UnitsDir()
	writeUnit(t, units, "future.service", valid+"FutureStop=C:\\Tools\\stop.exe\n")
	res, err := m.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) == 0 || !strings.Contains(strings.Join(res.Errors, "\n"), `error: unknown directive "FutureStop"`) {
		t.Fatalf("reload = %+v", res)
	}
	after, err := m.Status("")
	if err != nil {
		t.Fatal(err)
	}
	if after.Machine.ConfigRevision != before.Machine.ConfigRevision {
		t.Fatalf("rejected candidate replaced revision %s with %s", before.Machine.ConfigRevision, after.Machine.ConfigRevision)
	}
	if _, err := m.Status("future.service"); !isNotFound(err) {
		t.Fatalf("rejected unit loaded: %v", err)
	}

	writeUnit(t, units, "foo.service", valid+"FutureStop=C:\\Tools\\stop.exe\n")
	ver, err := m.Verify("foo.service")
	if err != nil {
		t.Fatal(err)
	}
	if ver.OK || len(ver.Issues) != 1 || ver.Issues[0].Severity != string(unit.SeverityError) ||
		!strings.Contains(ver.Issues[0].Message, `unknown directive "FutureStop"`) || filepath.Base(ver.Issues[0].Path) != "foo.service" {
		t.Fatalf("verify = %+v", ver)
	}
}

func isNotFound(err error) bool {
	var pe *protocol.Error
	return errors.As(err, &pe) && pe.Code == protocol.CodeNotFound
}
