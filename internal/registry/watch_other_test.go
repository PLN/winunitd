//go:build !windows

package registry

import (
	"strings"
	"testing"
)

func TestOpenWatchStub(t *testing.T) {
	t.Parallel()
	k, err := ParseKey(`HKLM\Software\Example`)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenWatch(k)
	if err == nil || w != nil {
		t.Fatalf("stub must not watch: w=%v err=%v", w, err)
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("err = %v", err)
	}
}
