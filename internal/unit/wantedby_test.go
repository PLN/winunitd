package unit

import (
	"slices"
	"strings"
	"testing"
)

// Enable creates enabled/<target>/<unit>, so every WantedBy= name must be one
// file-name component.
func TestWantedByRejectsPathTraversal(t *testing.T) {
	t.Parallel()
	bad := []string{
		`..\..\escape.target`,
		`../../escape.target`,
		`..\escape.target`,
		`sub/escape.target`,
		`sub\escape.target`,
		`\\server\share\escape.target`,
		`/abs/escape.target`,
		`C:escape.target`,
		`C:\escape.target`,
		`escape.target:stream`,
		`esc*ape.target`,
		`esc?ape.target`,
		`esc"ape.target`,
		`esc<ape.target`,
		`esc|ape.target`,
	}
	for _, value := range bad {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			src := "[Service]\nExecStart=C:\\App\\worker.exe\n\n[Install]\nWantedBy=" + value + " default.target\n"
			r := ParseUnit("worker.service", src)
			errs := r.Errors()
			if len(errs) != 1 {
				t.Fatalf("errors = %#v, want one WantedBy error", errs)
			}
			if errs[0].Line != 5 || !strings.Contains(errs[0].Message, "invalid WantedBy") {
				t.Fatalf("error = %#v", errs[0])
			}
			if r.Unit == nil {
				t.Fatal("no unit")
			}
			if !slices.Equal(r.Unit.WantedBy, []string{"default.target"}) {
				t.Fatalf("WantedBy = %#v, want only default.target", r.Unit.WantedBy)
			}
		})
	}
}

func TestWantedByAcceptsSingleComponentNames(t *testing.T) {
	t.Parallel()
	src := "[Service]\nExecStart=C:\\App\\worker.exe\n\n[Install]\nWantedBy=Default.target multi-user.target\nWantedBy=graphical-session.target worker-group\n"
	r := ParseUnit("worker.service", src)
	if r.HasError() {
		t.Fatalf("errors = %#v", r.Errors())
	}
	want := []string{"default.target", "multi-user.target", "graphical-session.target", "worker-group.service"}
	if !slices.Equal(r.Unit.WantedBy, want) {
		t.Fatalf("WantedBy = %#v, want %#v", r.Unit.WantedBy, want)
	}
	// An empty assignment still resets the list.
	r = ParseUnit("worker.service", src+"WantedBy=\n")
	if r.HasError() || len(r.Unit.WantedBy) != 0 {
		t.Fatalf("reset: WantedBy = %#v, errors = %#v", r.Unit.WantedBy, r.Errors())
	}
}

func TestCheckLinkName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"default.target", "a.b-c_d@e.target", "x.service"} {
		if err := CheckLinkName(name); err != nil {
			t.Errorf("CheckLinkName(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", ".", "..", `a\b.target`, "a/b.target", "c:x.target", "a\x00b.target", "a\tb.target", "a\x7fb.target"} {
		if err := CheckLinkName(name); err == nil {
			t.Errorf("CheckLinkName(%q) accepted", name)
		}
	}
}
