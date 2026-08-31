package runtime

import (
	"github.com/PLN/winunitd/internal/protocol"
)

// AccountLookup resolves a username or SID to UserInfo.
type AccountLookup func(name string) (UserInfo, error)

// FormatAccount is DOMAIN\user, or user when domain is empty.
func FormatAccount(info UserInfo) string {
	if info.Domain == "" {
		return info.Username
	}
	if info.Username == "" {
		return info.Domain
	}
	return info.Domain + `\` + info.Username
}

func validAccountSID(sid string) bool {
	return protocol.ValidSID(sid)
}
