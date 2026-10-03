package headless

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tail parser keeps each record's code and time and skips padding,
// partial lines and other text.
func TestLogTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	content := strings.Repeat(" ", daemonTailSize) + "\n" +
		`{"v":1,"timestamp":"2026-10-03T12:00:00Z","code":"daemon.close"}` + "\n" +
		"not json\n" +
		`{"v":1,"timestamp":"2026-10-03T12:00:01.5Z","code":"daemon.open"}` + "\n" +
		`{"v":1,"timestamp":"2026-10-03T12:00:02Z","co`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	tail, err := logTail(path)
	if err != nil || len(tail) != 2 || tail[0].Code != "daemon.close" || tail[1].Code != daemonOpenCode || tail[1].At != ft(1.5) {
		t.Fatalf("tail %+v %v", tail, err)
	}
}
