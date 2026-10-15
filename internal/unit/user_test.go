package unit

import "testing"

func TestUserDirective(t *testing.T) {
	const service = "[Service]\nExecStart=C:\\Apps\\work.exe\n"
	const format2 = "[Unit]\nFormatVersion=2\n"
	for _, tc := range []struct {
		name, unit string
		valid      bool
		user       string
		declared   bool
	}{
		{"absent", format2 + service, true, "", false},
		{"domain name", format2 + service + "User=EXAMPLE\\alice\n", true, "EXAMPLE\\alice", true},
		{"local name", format2 + service + "User=alice\n", true, "alice", true},
		{"sid", format2 + service + "User=S-1-5-21-1-2-3-1001\n", true, "S-1-5-21-1-2-3-1001", true},
		{"oneshot", format2 + service + "Type=oneshot\nUser=alice\n", true, "alice", true},
		{"notify", format2 + service + "Type=notify\nUser=alice\n", true, "alice", true},
		{"system", format2 + service + "User=SYSTEM\n", true, "", true},
		{"localsystem", format2 + service + "User=LocalSystem\n", true, "", true},
		{"nt authority system", format2 + service + "User=NT AUTHORITY\\System\n", true, "", true},
		{"system sid", format2 + service + "User=s-1-5-18\n", true, "", true},
		{"format 1", service + "User=alice\n", false, "", true},
		{"scm", format2 + "[Service]\nType=scm\nServiceName=Spooler\nUser=alice\n", false, "", true},
		{"empty", format2 + service + "User=\n", false, "", true},
		{"local service", format2 + service + "User=NT AUTHORITY\\LocalService\n", false, "", true},
		{"network service sid", format2 + service + "User=S-1-5-20\n", false, "", true},
		{"two backslashes", format2 + service + "User=A\\B\\c\n", false, "", true},
		{"bad sid", format2 + service + "User=S-1-x\n", false, "", true},
		{"twice", format2 + service + "User=alice\nUser=bob\n", false, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ParseUnit("work.service", tc.unit)
			if r.HasError() == tc.valid {
				t.Fatalf("validity: %+v", r.Issues)
			}
			if !tc.valid {
				return
			}
			if r.Unit.Service.User != tc.user || r.Unit.Service.UserDeclared != tc.declared {
				t.Fatalf("User=%q declared=%t, want %q %t", r.Unit.Service.User, r.Unit.Service.UserDeclared, tc.user, tc.declared)
			}
		})
	}
}

func TestUserScopeIssues(t *testing.T) {
	r := ParseUnit("work.service", "[Unit]\nFormatVersion=2\n[Service]\nExecStart=C:\\Apps\\work.exe\nUser=SYSTEM\n")
	if r.HasError() {
		t.Fatal(r.Issues)
	}
	if len(UserScopeIssues(r.Unit, false)) != 0 {
		t.Fatal("system scope rejected User=")
	}
	if len(UserScopeIssues(r.Unit, true)) != 1 {
		t.Fatal("user scope accepted User=, even a SYSTEM spelling")
	}
}
