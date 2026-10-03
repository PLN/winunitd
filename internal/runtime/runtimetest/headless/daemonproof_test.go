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

// The bounded test-output buffer keeps at most its bound and says so.
func TestBoundedBuffer(t *testing.T) {
	var b boundedBuffer
	if n, err := b.Write(make([]byte, MaxFileBytes-1)); n != MaxFileBytes-1 || err != nil || b.truncated {
		t.Fatal("a write within the bound")
	}
	if n, err := b.Write([]byte("abc")); n != 3 || err != nil || !b.truncated || b.Len() != MaxFileBytes {
		t.Fatalf("a write past the bound: %d bytes kept, truncated %t", b.Len(), b.truncated)
	}
}
