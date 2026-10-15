package runtime

import "strings"

// ProfileKeeperFlag is the daemon's profile-keeper mode: the process does
// nothing until it is killed. Started with a Windows-owned profile, it keeps
// that profile loaded for the processes of a User= unit.
const ProfileKeeperFlag = "--profile-keeper"

// AccountSession is the identity of one invocation of a system unit that runs
// as another account (User=): an S4U token, a private desktop and a profile
// keeper. Processes started with it run as that account until Close.
type AccountSession struct {
	// Info identifies the account the token belongs to.
	Info UserInfo
	env  []string
	plat accountPlatform
}

// AccountSessionSpec selects the account and the daemon executable that serves
// the desktop helper and the profile keeper.
type AccountSessionSpec struct {
	Account string     // DOMAIN\name, name or SID
	Exe     string     // absolute path of winunitd.exe
	Daemon  *DaemonJob // the manager's self-assigned broker job
}

// AccountError reports that a User= account could not be logged on or
// prepared, so no process was started. The unit's Restart= policy applies.
type AccountError struct {
	Account string
	Err     error
}

func (e *AccountError) Error() string { return "User=" + e.Account + ": " + e.Err.Error() }

func (e *AccountError) Unwrap() error { return e.Err }

// OverlayEnv returns base with every KEY=value of over applied, matching keys
// case-insensitively as Windows does.
func OverlayEnv(base, over []string) []string {
	out := append([]string(nil), base...)
	index := make(map[string]int, len(out))
	for i, e := range out {
		name, _, _ := strings.Cut(e, "=")
		index[strings.ToUpper(name)] = i
	}
	for _, e := range over {
		name, _, _ := strings.Cut(e, "=")
		if i, ok := index[strings.ToUpper(name)]; ok {
			out[i] = e
			continue
		}
		index[strings.ToUpper(name)] = len(out)
		out = append(out, e)
	}
	return out
}

// Environment returns the account's environment for a unit process, before
// the unit's own Environment= is applied.
func (s *AccountSession) Environment() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.env...)
}
