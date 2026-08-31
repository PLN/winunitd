package runtime

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	credManScheme = "credman"
	lsaScheme     = "lsa"
)

// ValidateCredentialURI accepts a named CredMan or LSA reference for a
// linger record. The URI is not a secret: passwords, file paths, and
// env references are rejected.
func ValidateCredentialURI(raw string) error {
	_, err := ParseCredentialURI(raw)
	return err
}

// ParseCredentialURI returns the canonical named credential URI
// (credman://… or lsa://…). Empty input is valid (no URI).
func ParseCredentialURI(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	lower := strings.ToLower(s)
	if strings.Contains(lower, "password") || strings.Contains(lower, "passwd") || strings.Contains(lower, "secret") {
		return "", fmt.Errorf("credential URI must not contain a secret")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid credential URI: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != credManScheme && scheme != lsaScheme {
		return "", fmt.Errorf("credential URI must use credman:// or lsa://")
	}
	if u.Opaque != "" {
		return "", fmt.Errorf("credential URI must use host/path form")
	}
	name := u.Host
	if u.Path != "" && u.Path != "/" {
		if name == "" {
			name = strings.TrimPrefix(u.Path, "/")
		} else {
			name = name + u.Path
		}
	}
	name = strings.Trim(name, "/")
	if name == "" {
		return "", fmt.Errorf("credential URI name required")
	}
	if u.User != nil {
		return "", fmt.Errorf("credential URI must not embed userinfo")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("credential URI must not contain query or fragment")
	}
	return scheme + "://" + name, nil
}

func credentialURIParts(raw string) (scheme, name string, err error) {
	canon, err := ParseCredentialURI(raw)
	if err != nil || canon == "" {
		return "", "", err
	}
	scheme, name, _ = strings.Cut(canon, "://")
	return scheme, name, nil
}
