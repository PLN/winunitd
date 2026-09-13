//go:build windows

package runtime

import "strings"

func applyAccountName(info *UserInfo, name string) {
	if info == nil || strings.TrimSpace(name) == "" || info.Username != "" {
		return
	}
	if i := strings.LastIndex(name, `\`); i >= 0 {
		info.Domain = name[:i]
		info.Username = name[i+1:]
		return
	}
	info.Username = name
}

func credentialURIParts(raw string) (scheme, name string, err error) {
	canon, err := ParseCredentialURI(raw)
	if err != nil || canon == "" {
		return "", "", err
	}
	scheme, name, _ = strings.Cut(canon, "://")
	return scheme, name, nil
}

type tokenCleanupFunc func() error

func (f tokenCleanupFunc) Close() error { return f() }
