package runtime

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

// Environment returns the account's environment for a unit process, before
// the unit's own Environment= is applied.
func (s *AccountSession) Environment() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.env...)
}
