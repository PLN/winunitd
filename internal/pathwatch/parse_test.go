package pathwatch

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()
	ok, err := Parse(`C:\Data\incoming`)
	if err != nil {
		t.Fatal(err)
	}
	if ok.Raw != `C:\Data\incoming` {
		t.Fatalf("raw = %q", ok.Raw)
	}

	unc, err := Parse(`\\server\share\in`)
	if err != nil {
		t.Fatal(err)
	}
	if unc.Raw != `\\server\share\in` {
		t.Fatalf("unc = %q", unc.Raw)
	}

	exists, err := ParseExists(`C:\Data\ready.flag`)
	if err != nil {
		t.Fatal(err)
	}
	if exists.Raw != `C:\Data\ready.flag` {
		t.Fatalf("exists = %q", exists.Raw)
	}

	fwd, err := Parse(`C:/Data/incoming`)
	if err != nil {
		t.Fatal(err)
	}
	if fwd.Raw != `C:/Data/incoming` {
		t.Fatalf("fwd = %q", fwd.Raw)
	}

	tests := []struct {
		raw     string
		wantErr string
	}{
		{"", "empty path"},
		{`incoming`, "absolute Windows path"},
		{`.\incoming`, "absolute Windows path"},
		{`Data\incoming`, "absolute Windows path"},
		{`/tmp/incoming`, "absolute Windows path"},
		{`C:incoming`, "absolute Windows path"},
	}
	for _, tt := range tests {
		t.Run("changed "+tt.raw, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(tt.raw)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
		t.Run("exists "+tt.raw, func(t *testing.T) {
			t.Parallel()
			_, err := ParseExists(tt.raw)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestSplitDirName(t *testing.T) {
	t.Parallel()
	dir, name := SplitDirName(`C:\Data\incoming\file.txt`)
	if dir != `C:\Data\incoming` || name != "file.txt" {
		t.Fatalf("dir=%q name=%q", dir, name)
	}
	dir, name = SplitDirName(`C:\file.txt`)
	if dir != `C:\` || name != "file.txt" {
		t.Fatalf("root dir=%q name=%q", dir, name)
	}
	dir, name = SplitDirName(`C:\Data\incoming\`)
	if dir != `C:\Data` || name != "incoming" {
		t.Fatalf("trail dir=%q name=%q", dir, name)
	}
}
