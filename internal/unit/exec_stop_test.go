package unit

import (
	"reflect"
	"strings"
	"testing"
)

func TestExecStopCommandGrammarAndUnsupportedForms(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantError string
		want                  []string
	}{
		{"quoted", `ExecStop="C:\Program Files\Tools\stop.exe" --wait`, "", []string{`C:\Program Files\Tools\stop.exe`, "--wait"}},
		{"argv", "ExecStop=C:\\Tools\\stop.exe\nExecStopArg=--wait\nExecStopArg=two words", "", []string{`C:\Tools\stop.exe`, "--wait", "two words"}},
		{"relative", "ExecStop=stop.exe", "absolute path", nil},
		{"empty", "ExecStop=", "requires an executable", nil},
		{"args-only", "ExecStopArg=--wait", "requires an executable", nil},
		{"multiple", "ExecStop=C:\\Tools\\one.exe\nExecStop=C:\\Tools\\two.exe", "multiple ExecStop", nil},
		{"zero-timeout", "ExecStop=C:\\Tools\\stop.exe\nTimeoutStopSec=0", "positive TimeoutStopSec", nil},
		{"native", "Type=scm\nServiceName=Fixture\nExecStop=C:\\Tools\\stop.exe", "not valid for Type=scm", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := ParseUnit("work.service", "[Service]\nExecStart=C:\\Tools\\work.exe\n"+tc.body+"\n")
			if tc.wantError != "" {
				for _, issue := range rep.Errors() {
					if strings.Contains(issue.Message, tc.wantError) {
						return
					}
				}
				t.Fatalf("missing %q: %v", tc.wantError, rep.Errors())
			}
			if rep.HasError() || !reflect.DeepEqual(rep.Unit.Service.ExecStop, tc.want) {
				t.Fatalf("parsed command: %+v %v", rep.Unit, rep.Errors())
			}
		})
	}
}
