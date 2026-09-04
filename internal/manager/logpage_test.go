package manager

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
)

func TestLogsPagesStayWithinRPCBudget(t *testing.T) {
	m := testManager(t, map[string]string{"foo.service": "[Service]\nExecStart=C:\\Tools\\foo.exe\nWorkingDirectory=C:\\Tools\n"})
	var lines strings.Builder
	const count = 1100
	for i := 0; i < count; i++ {
		fmt.Fprintf(&lines, "%04d:%s\n", i, strings.Repeat("<&>", 350))
	}
	m.journal.Attach("foo.service", 123, "test", strings.NewReader(lines.String()), nil)
	m.journal.Wait("foo.service")
	c, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	cursor, total, pages := "", 0, 0
	for {
		result, err := c.Logs(context.Background(), protocol.LogsParams{Unit: "foo.service", Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, e := range result.Entries {
			if !strings.HasPrefix(e.Message, fmt.Sprintf("%04d:", total)) {
				t.Fatalf("lost or repeated record at %d", total)
			}
			total++
		}
		if !result.More {
			break
		}
		if result.Cursor == cursor || pages > count {
			t.Fatal("pagination made no progress")
		}
		cursor = result.Cursor
	}
	if total != count || pages < 2 {
		t.Fatalf("got %d records in %d pages", total, pages)
	}
}
