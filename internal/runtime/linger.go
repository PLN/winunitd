package runtime

import (
	"errors"
	"fmt"
)

// ErrNoLingerToken is returned when S4U cannot produce a linger token.
// There is no password fallback.
var ErrNoLingerToken = errors.New("no linger token (S4U failed; fail closed)")

// LingerRecord is the tiny on-disk linger document (not an NTFS symlink).
// CredentialURI is an optional named CredMan/LSA reference, never a secret.
type LingerRecord struct {
	SID           string
	Name          string
	CredentialURI string
}

// LingerTokenFunc obtains a token for a lingering user manager (no session).
// Production uses a trusted LSA S4U first, then a named CredMan/LSA URI
// only if the URI is present and S4U is insufficient for outbound
// network credentials (LOGON32_LOGON_BATCH on that fallback).
// A non-nil token returned with an error carries unfinished cleanup ownership.
type LingerTokenFunc func(rec LingerRecord) (*UserToken, error)

func failLinger(sid string, err error) error {
	if err == nil {
		return fmt.Errorf("%w: SID %s", ErrNoLingerToken, sid)
	}
	return fmt.Errorf("%w: SID %s: %v", ErrNoLingerToken, sid, err)
}
