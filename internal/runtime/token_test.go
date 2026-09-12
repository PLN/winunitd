package runtime

import (
	"errors"
	"strings"
	"testing"
)

type retryTokenCloser struct{ calls int }

func (c *retryTokenCloser) Close() error {
	c.calls++
	if c.calls == 1 {
		return errors.New("injected close failure")
	}
	return nil
}

func TestTokenCloseRetainsFailureAndReleasesOnce(t *testing.T) {
	c := &retryTokenCloser{}
	tok := &UserToken{native: c}
	if err := tok.Close(); err == nil || tok.native != c {
		t.Fatal("failed close lost ownership")
	}
	if err := tok.Close(); err != nil || tok.native != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := tok.Close(); err != nil || c.calls != 2 {
		t.Fatalf("closed token reused: calls=%d err=%v", c.calls, err)
	}
}

func TestQueryUserTokenFailsClosedWithoutWTS(t *testing.T) {
	// On Linux this is a stub. On Windows CI without SeTcbPrivilege,
	// WTSQueryUserToken also fails. Either way: no fallback token.
	tok, err := QueryUserToken(1)
	if tok != nil {
		_ = tok.Close()
		if err == nil {
			// LocalSystem / TCB: success is allowed, but this test
			// still asserts the error type when it fails.
			return
		}
	}
	if err == nil {
		t.Fatal("expected fail-closed error")
	}
	if !errors.Is(err, ErrNoUserToken) {
		t.Fatalf("err = %v, want ErrNoUserToken", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "password") {
		t.Fatalf("error must not mention passwords: %v", err)
	}
}

func TestTokenSourceContractNoFallback(t *testing.T) {
	t.Parallel()
	err := failClosed(7, errors.New("access denied"))
	if !errors.Is(err, ErrNoUserToken) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "session 7") {
		t.Fatalf("err = %v", err)
	}
}
