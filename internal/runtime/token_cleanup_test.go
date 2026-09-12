package runtime

import (
	"errors"
	"io"
	"testing"
)

type countedTokenResource struct {
	calls int
	fail  bool
}

func (c *countedTokenResource) Close() error {
	c.calls++
	if c.fail {
		return errors.New("injected native resource close failure")
	}
	return nil
}

func TestTokenAuxiliaryCleanupRetainsOnlyFailures(t *testing.T) {
	primary, raw, buffer, connection := &countedTokenResource{}, &countedTokenResource{fail: true}, &countedTokenResource{}, &countedTokenResource{fail: true}
	tok := &UserToken{native: primary, cleanup: []io.Closer{connection, buffer, raw}}
	if err := tok.Close(); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	if tok.native != nil || primary.calls != 1 || buffer.calls != 1 || raw.calls != 1 || connection.calls != 1 {
		t.Fatal("initial cleanup failed to account for each resource")
	}
	raw.fail = false
	if err := tok.Close(); err == nil {
		t.Fatal("connection failure hidden")
	}
	connection.fail = false
	if err := tok.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tok.Close(); err != nil {
		t.Fatal(err)
	}
	if primary.calls != 1 || buffer.calls != 1 || raw.calls != 2 || connection.calls != 3 || len(tok.cleanup) != 0 {
		t.Fatal("retry closed released resources or lost an obligation")
	}
}

func TestTokenAcquisitionCleanupFailurePreventsLaunch(t *testing.T) {
	primary, aux := &countedTokenResource{}, &countedTokenResource{fail: true}
	owner := &UserToken{native: primary, cleanup: []io.Closer{aux}}
	tok, err := finishTokenAcquisition(owner, nil)
	if err == nil || tok != owner || tok.native != nil || primary.calls != 1 {
		t.Fatal("temporary cleanup failure returned a usable token or dropped ownership")
	}
	aux.fail = false
	if err := tok.Close(); err != nil {
		t.Fatal(err)
	}
	if len(tok.cleanup) != 0 || primary.calls != 1 {
		t.Fatal("cleanup retry failed")
	}
}

func TestTokenAcquisitionSuccessKeepsOnlyPrimary(t *testing.T) {
	primary, aux := &countedTokenResource{}, &countedTokenResource{}
	owner := &UserToken{native: primary, cleanup: []io.Closer{aux}}
	tok, err := finishTokenAcquisition(owner, nil)
	if err != nil || tok != owner || primary.calls != 0 || aux.calls != 1 || len(tok.cleanup) != 0 {
		t.Fatal("acquisition did not transfer only primary ownership")
	}
	if err := tok.Close(); err != nil {
		t.Fatal(err)
	}
	if primary.calls != 1 || aux.calls != 1 {
		t.Fatal("transferred resources closed incorrectly")
	}
}

func TestTokenAcquisitionErrorReleasesAllResources(t *testing.T) {
	primary, aux := &countedTokenResource{}, &countedTokenResource{}
	lookupErr := errors.New("identity lookup failed")
	tok, err := finishTokenAcquisition(&UserToken{native: primary, cleanup: []io.Closer{aux}}, lookupErr)
	if tok != nil || !errors.Is(err, lookupErr) || primary.calls != 1 || aux.calls != 1 {
		t.Fatal("failed acquisition did not release ownership and preserve cause")
	}
}

func TestTokenAttemptPreservesConnectionAndFailedResources(t *testing.T) {
	connection, buffer, raw, primary := &countedTokenResource{}, &countedTokenResource{fail: true}, &countedTokenResource{}, &countedTokenResource{fail: true}
	owner := &UserToken{native: primary, cleanup: []io.Closer{connection, buffer, raw}}
	if err := closeTokenAttempt(owner, 1); err == nil {
		t.Fatal("attempt cleanup failure hidden")
	}
	if connection.calls != 0 || raw.calls != 1 || owner.native != primary {
		t.Fatal("attempt closed shared connection or lost primary")
	}
	buffer.fail = false
	primary.fail = false
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if connection.calls != 1 || buffer.calls != 2 || primary.calls != 2 || raw.calls != 1 {
		t.Fatal("final acquisition cleanup did not own exactly remaining resources")
	}
}

func TestTokenCredentialReplacementRetainsOldToken(t *testing.T) {
	oldPrimary, newPrimary := &countedTokenResource{fail: true}, &countedTokenResource{}
	old := &UserToken{native: oldPrimary}
	next := &UserToken{native: newPrimary, cleanup: []io.Closer{old}}
	tok, err := finishTokenAcquisition(next, nil)
	if err == nil || tok != next || newPrimary.calls != 1 || old.native != oldPrimary {
		t.Fatal("replacement lost old token cleanup or returned launch authority")
	}
	oldPrimary.fail = false
	if err := tok.Close(); err != nil {
		t.Fatal(err)
	}
	if old.native != nil || newPrimary.calls != 1 {
		t.Fatal("replacement retry closed wrong resources")
	}
}
