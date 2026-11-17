package unit

import (
	"strings"
	"unicode"
)

// Account spellings that User= does not log on by S4U. They are compared
// lowercased, with or without the NT AUTHORITY domain.
var (
	systemAccountNames  = []string{"system", "localsystem", "s-1-5-18"}
	serviceAccountNames = []string{"localservice", "local service", "networkservice", "network service", "s-1-5-19", "s-1-5-20"}
)

// finishUser checks User=, the account a system unit's processes run as. The
// format and type rules apply to the directive's presence, so they come first;
// a SYSTEM spelling then selects the ordinary LocalSystem launch.
func (p *parser) finishUser(spec *ServiceSpec, s *serviceBuilder) {
	if s.userL == 0 {
		return
	}
	spec.UserDeclared = true
	value := strings.TrimSpace(s.user)
	switch {
	case p.unit.FormatVersion != 2:
		p.errorf(s.userL, "User requires FormatVersion=2")
		return
	case spec.Type != TypeSimple && spec.Type != TypeOneshot && spec.Type != TypeNotify:
		p.errorf(s.userL, "User is supported only for Type=simple, oneshot and notify")
		return
	case value == "":
		p.errorf(s.userL, `User must name an account: DOMAIN\name, a local name or a SID`)
		return
	}
	name := strings.ToLower(value)
	name = strings.TrimPrefix(name, `nt authority\`)
	switch {
	case containsName(systemAccountNames, name):
		return // the default launch as LocalSystem
	case containsName(serviceAccountNames, name):
		p.errorf(s.userL, "User=%s: LocalService and NetworkService are not supported", value)
		return
	}
	if strings.Count(value, `\`) > 1 || strings.HasPrefix(value, `\`) || strings.HasSuffix(value, `\`) ||
		strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) || r == '/' }) {
		p.errorf(s.userL, `User=%s is not an account name: use DOMAIN\name, a local name or a SID`, value)
		return
	}
	if strings.HasPrefix(name, "s-") {
		if !validSIDString(value) {
			p.errorf(s.userL, "User=%s is not a valid SID", value)
			return
		}
		value = "S" + value[1:] // the runtime takes only the canonical form as a SID
	}
	spec.User = value
}

// finishUserLogon checks UserLogon=, where each start of a User= unit gets its
// logon: s4u (the default) or session-or-s4u. It applies only beside a User=
// that passed its own rules and names an account other than SYSTEM.
func (p *parser) finishUserLogon(spec *ServiceSpec, s *serviceBuilder) {
	if s.userLogonL == 0 {
		return
	}
	value := strings.ToLower(strings.TrimSpace(s.userLogon))
	if value != "s4u" && value != "session-or-s4u" {
		p.errorf(s.userLogonL, "UserLogon must be s4u or session-or-s4u")
		return
	}
	if spec.User == "" {
		p.errorf(s.userLogonL, "UserLogon requires User= naming an account other than SYSTEM")
		return
	}
	spec.UserSession = value == "session-or-s4u"
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// validSIDString accepts S-1-<authority>-<subauthority>... with decimal parts.
func validSIDString(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) < 3 || !strings.EqualFold(parts[0], "S") || parts[1] != "1" {
		return false
	}
	for _, part := range parts[2:] {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// UserScopeIssues rejects User= in a user unit: a user manager already runs
// as its user.
func UserScopeIssues(u *Unit, userScope bool) []Issue {
	if !userScope || u == nil || u.Service == nil || !u.Service.UserDeclared {
		return nil
	}
	return []Issue{{
		Path:     u.Path,
		Severity: SeverityError,
		Message:  "User is only supported in system units; a user manager runs as its user",
	}}
}
