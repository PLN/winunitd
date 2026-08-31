package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// UserInfo is the identity used to build a deterministic user environment
// (DESIGN.md §58). It is not an Explorer interactive dump.
type UserInfo struct {
	SID      string
	Username string
	Domain   string
	Profile  string
}

// UserEnvKeys are the user-specific variables set for user managers and
// the units they start (DESIGN.md §58).
var UserEnvKeys = []string{
	"USERPROFILE",
	"LOCALAPPDATA",
	"APPDATA",
	"TEMP",
	"TMP",
	"USERNAME",
	"USERDOMAIN",
}

// interactiveDumpKeys are session/interactive variables that must not be
// copied from the parent (Explorer or LocalSystem) into a user manager.
var interactiveDumpKeys = []string{
	"SESSIONNAME",
	"CLIENTNAME",
	"HOMEDRIVE",
	"HOMEPATH",
	"LOGONSERVER",
	"USERDOMAIN_ROAMINGPROFILE",
}

// UserEnvVars returns the seven DESIGN.md §58 assignments for info.
func UserEnvVars(info UserInfo) []string {
	localApp := filepath.Join(info.Profile, "AppData", "Local")
	roaming := filepath.Join(info.Profile, "AppData", "Roaming")
	temp := filepath.Join(localApp, "Temp")
	return []string{
		"USERPROFILE=" + info.Profile,
		"LOCALAPPDATA=" + localApp,
		"APPDATA=" + roaming,
		"TEMP=" + temp,
		"TMP=" + temp,
		"USERNAME=" + info.Username,
		"USERDOMAIN=" + info.Domain,
	}
}

// MergeDeterministicUserEnv copies parent, drops user-specific and
// interactive-session variables, then sets the §58 keys from info.
func MergeDeterministicUserEnv(parent []string, info UserInfo) []string {
	drop := make(map[string]struct{}, len(UserEnvKeys)+len(interactiveDumpKeys))
	for _, k := range UserEnvKeys {
		drop[envKey(k)] = struct{}{}
	}
	for _, k := range interactiveDumpKeys {
		drop[envKey(k)] = struct{}{}
	}
	out := make([]string, 0, len(parent)+len(UserEnvKeys))
	for _, e := range parent {
		name, _, _ := strings.Cut(e, "=")
		if _, ok := drop[envKey(name)]; ok {
			continue
		}
		out = append(out, e)
	}
	return append(out, UserEnvVars(info)...)
}

// ApplyUserEnv overwrites the current process environment with the §58
// keys from info and unsets interactive-dump variables. Used by the
// user-manager process after CreateProcessAsUser (or a test exec).
func ApplyUserEnv(info UserInfo) error {
	for _, k := range interactiveDumpKeys {
		if err := os.Unsetenv(k); err != nil {
			return err
		}
	}
	for _, e := range UserEnvVars(info) {
		name, val, _ := strings.Cut(e, "=")
		if err := os.Setenv(name, val); err != nil {
			return err
		}
	}
	return nil
}

func envKey(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return unicode.ToUpper(r)
		}
		return r
	}, name)
}
