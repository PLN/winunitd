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

func TestOpenExistsWatchStub(t *testing.T) {
	t.Parallel()
	s, err := ParseExists(`C:\Data\incoming`)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenExistsWatch(s)
	if err == nil || w != nil {
		t.Fatalf("stub must not watch: w=%v err=%v", w, err)
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("err = %v", err)
	}
}

func TestExistsStub(t *testing.T) {
	t.Parallel()
	s, err := ParseExists(`C:\Data\incoming`)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := Exists(s)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("stub Exists must be false")
	}
}
