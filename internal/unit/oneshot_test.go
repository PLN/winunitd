package unit

import (
	"strings"
	"testing"
)

func TestRemainAfterExit(t *testing.T) {
	for _, tc := range []struct {
		name, typ, directive string
		want, invalid        bool
	}{
		{"default", "oneshot", "", false, false},
		{"no", "oneshot", "RemainAfterExit=no", false, false},
		{"yes", "oneshot", "RemainAfterExit=yes", true, false},
		{"last wins", "oneshot", "RemainAfterExit=yes\nRemainAfterExit=no", false, false},
		{"invalid", "oneshot", "RemainAfterExit=maybe", false, true},
		{"empty", "oneshot", "RemainAfterExit=", false, true},
		{"simple", "simple", "RemainAfterExit=yes", true, true},
		{"notify", "notify", "RemainAfterExit=no", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Parse("work.service", "work.service", []byte("[Service]\nType="+tc.typ+"\nExecStart=C:\\Tools\\work.exe\n"+tc.directive+"\n"))
			invalid := false
			for _, issue := range r.Issues {
				if issue.Severity == SeverityError && strings.Contains(issue.Message, "RemainAfterExit") {
					invalid = true
				}
			}
			if invalid != tc.invalid {
				t.Fatalf("issues = %+v", r.Issues)
			}
			if !invalid && r.Unit.Service.RemainAfterExit != tc.want {
				t.Fatalf("RemainAfterExit = %v", r.Unit.Service.RemainAfterExit)
			}
		})
	}
}
