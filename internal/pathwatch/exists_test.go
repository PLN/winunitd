package pathwatch

import "testing"

func TestJoinDirName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		dir, name, want string
	}{
		{`C:\Data`, "flag", `C:\Data\flag`},
		{`C:\`, "flag", `C:\flag`},
		{`C:/Data`, "sub", `C:/Data\sub`},
		{`\\server\share`, "dir", `\\server\share\dir`},
	}
	for _, tt := range tests {
		if got := joinDirName(tt.dir, tt.name); got != tt.want {
			t.Errorf("joinDirName(%q, %q) = %q, want %q", tt.dir, tt.name, got, tt.want)
		}
	}
}

func TestIsTargetLeafWatch(t *testing.T) {
	t.Parallel()
	if !isTargetLeafWatch(`C:\Data\ready.flag`, `C:\Data`, "ready.flag") {
		t.Fatal("parent+basename must be a leaf watch")
	}
	if !isTargetLeafWatch(`C:\Data\ready.flag`, `C:/Data`, "READY.FLAG") {
		t.Fatal("slash and case differences must still be a leaf watch")
	}
	if isTargetLeafWatch(`C:\Data\sub\ready.flag`, `C:\Data`, "sub") {
		t.Fatal("intermediate component must not be a leaf watch")
	}
	if !isTargetLeafWatch(`C:\flag`, `C:\`, "flag") {
		t.Fatal("drive-root parent must be a leaf watch")
	}
}

func TestNextExistsStep(t *testing.T) {
	t.Parallel()
	dir, filter, ok := nextExistsStep(`C:\Data\sub\ready.flag`, `C:\Data`, "sub")
	if !ok || dir != `C:\Data\sub` || filter != "ready.flag" {
		t.Fatalf("step = %q %q ok=%v", dir, filter, ok)
	}

	if _, _, ok := nextExistsStep(`C:\Data\ready.flag`, `C:\Data`, "ready.flag"); ok {
		t.Fatal("leaf watch must not step closer")
	}
	if _, _, ok := nextExistsStep(`C:\Data\sub\ready.flag`, `C:\Data`, "other"); ok {
		t.Fatal("sibling basename must not step closer")
	}

	dir, filter, ok = nextExistsStep(`C:\a\b\c\flag`, `C:\a`, "b")
	if !ok || dir != `C:\a\b` || filter != "c" {
		t.Fatalf("mid step = %q %q ok=%v", dir, filter, ok)
	}

	dir, filter, ok = nextExistsStep(`\\srv\sh\sub\flag`, `\\srv\sh`, "sub")
	if !ok || dir != `\\srv\sh\sub` || filter != "flag" {
		t.Fatalf("unc step = %q %q ok=%v", dir, filter, ok)
	}
}
