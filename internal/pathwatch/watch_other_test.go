//go:build !windows

package pathwatch

import (
	"strings"
	"testing"
)

func TestOpenWatchStub(t *testing.T) {
	t.Parallel()
	s, err := Parse(`C:\Data\incoming`)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenWatch(s)
	if err == nil || w != nil {
		t.Fatalf("stub must not watch: w=%v err=%v", w, err)
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("err = %v", err)
	}
}
