//go:build !windows

package protocol

// DefaultAuthorizer denies all peers. Production control uses the Windows named pipe.
func DefaultAuthorizer() Authorizer {
	return DenyAll
}

// UserAuthorizer denies all peers. Production control uses the Windows named pipe.
func UserAuthorizer(ownerSID string) Authorizer {
	_ = ownerSID
	return DenyAll
}
