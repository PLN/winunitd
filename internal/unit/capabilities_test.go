package unit

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestFormatVersionsMatchParser(t *testing.T) {
	t.Parallel()
	got := FormatVersions()
	if !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("FormatVersions() = %v", got)
	}
	got[0] = 99
	if FormatVersions()[0] != 1 {
		t.Fatal("FormatVersions exposes the parser table")
	}
	for _, v := range FormatVersions() {
		r := ParseUnit("v.service", "[Unit]\nFormatVersion="+strconv.Itoa(v)+"\n[Service]\nExecStart=C:\\Apps\\worker.exe\nWorkingDirectory=C:\\Apps\n")
		if r.HasError() || r.Unit.FormatVersion != v {
			t.Fatalf("FormatVersion=%d: %+v", v, r.Issues)
		}
	}
	for _, value := range []string{"0", "3", "01", "+2", "2.0", " 2x", "two"} {
		r := ParseUnit("v.service", "[Unit]\nFormatVersion="+value+"\n[Service]\nExecStart=C:\\Apps\\worker.exe\nWorkingDirectory=C:\\Apps\n")
		errs := r.Errors()
		if len(errs) != 1 || !strings.Contains(errs[0].Message, "(supported: 1, 2)") {
			t.Fatalf("FormatVersion=%q: %+v", value, r.Issues)
		}
	}
}

func TestDirectivesMatchParserAndAreCopies(t *testing.T) {
	t.Parallel()
	got := Directives()
	if len(got) != len(knownDirectives) {
		t.Fatalf("sections = %d, want %d", len(got), len(knownDirectives))
	}
	for section, names := range got {
		if !slices.IsSorted(names) || len(names) != len(knownDirectives[section]) {
			t.Fatalf("[%s] = %v", section, names)
		}
		for _, name := range names {
			if !knownDirectives[section][name] {
				t.Fatalf("[%s] %s is not recognized by the parser", section, name)
			}
		}
	}
	for _, want := range []string{"ExecStop", "RestartBackoff", "RestartMaxDelaySec", "RemainAfterExit", "MemoryMax"} {
		if !slices.Contains(got["Service"], want) {
			t.Fatalf("[Service] lacks %s: %v", want, got["Service"])
		}
	}
	if !slices.Contains(got["Unit"], "FormatVersion") {
		t.Fatalf("[Unit] lacks FormatVersion: %v", got["Unit"])
	}
	got["Service"][0] = "Mutated"
	if slices.Contains(Directives()["Service"], "Mutated") {
		t.Fatal("Directives exposes the parser table")
	}
}
