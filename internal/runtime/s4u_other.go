//go:build !windows

package runtime

import "fmt"

// ObtainLingerToken is only available on Windows (trusted LSA S4U).
// Tests inject a LingerTokenFunc; production never falls back to a password.
func ObtainLingerToken(rec LingerRecord) (*UserToken, error) {
	return nil, failLinger(rec.SID, fmt.Errorf("S4U logon is only available on Windows"))
}
