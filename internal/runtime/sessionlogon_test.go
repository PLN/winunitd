package runtime

import (
	"errors"
	"io"
	"slices"
	"testing"
)

const (
	aliceSID = "S-1-5-21-1-2-3-1001"
	bobSID   = "S-1-5-21-1-2-3-1002"
)

type fakeHandle struct {
	name string
	sid  string
	api  *fakeSessionAPI
}

func (h *fakeHandle) Close() error {
	if err := h.api.closeErr[h.name]; err != nil {
		return err
	}
	h.api.closed = append(h.api.closed, h.name)
	return nil
}

// fakeSessionAPI hands out named handles: "session N" for a session's token,
// "linked N", "primary N" for the copies made from it.
type fakeSessionAPI struct {
	sessions   []loggedOnSession
	users      map[uint32]string // session -> SID; absent means nobody signed in
	queryErr   map[uint32]error
	filtered   map[uint32]bool
	linkedErr  error
	closeErr   map[string]error
	opened     []string
	closed     []string
	duplicated []string
}

func (f *fakeSessionAPI) handle(name, sid string) *fakeHandle {
	f.opened = append(f.opened, name)
	return &fakeHandle{name: name, sid: sid, api: f}
}

func (f *fakeSessionAPI) api() sessionLogonAPI {
	session := func(c io.Closer) string { return c.(*fakeHandle).name }
	return sessionLogonAPI{
		sessions: func() ([]loggedOnSession, error) { return f.sessions, nil },
		token: func(id uint32) (io.Closer, error) {
			if err := f.queryErr[id]; err != nil {
				return nil, err
			}
			sid, ok := f.users[id]
			if !ok {
				return nil, errNoSessionUser
			}
			return f.handle("session "+string(rune('0'+id)), sid), nil
		},
		userSID: func(c io.Closer) (string, error) { return c.(*fakeHandle).sid, nil },
		filtered: func(c io.Closer) (bool, error) {
			name := session(c)
			return f.filtered[uint32(name[len(name)-1]-'0')], nil
		},
		linked: func(c io.Closer) (io.Closer, error) {
			if f.linkedErr != nil {
				return nil, f.linkedErr
			}
			name := session(c)
			return f.handle("linked "+name[len(name)-1:], c.(*fakeHandle).sid), nil
		},
		duplicate: func(c io.Closer) (io.Closer, error) {
			name := session(c)
			f.duplicated = append(f.duplicated, name)
			return f.handle("primary "+name[len(name)-1:], c.(*fakeHandle).sid), nil
		},
		setSession: func(io.Closer) error { return nil },
	}
}

func (f *fakeSessionAPI) allClosedBut(t *testing.T, keep ...string) {
	t.Helper()
	for _, name := range f.opened {
		if slices.Contains(keep, name) == slices.Contains(f.closed, name) {
			t.Fatalf("opened %v, closed %v, want all but %v closed", f.opened, f.closed, keep)
		}
	}
}

func TestSessionSelectionOrderAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sessions []loggedOnSession
		users    map[uint32]string
		want     uint32
	}{
		{"lowest active", []loggedOnSession{{ID: 5}, {ID: 4, Active: true}, {ID: 2, Active: true}, {ID: 1}}, map[uint32]string{1: aliceSID, 2: aliceSID, 4: aliceSID, 5: aliceSID}, 2},
		{"lowest disconnected", []loggedOnSession{{ID: 5}, {ID: 3}, {ID: 1, Active: true}}, map[uint32]string{1: bobSID, 3: aliceSID, 5: aliceSID}, 3},
		{"other user active", []loggedOnSession{{ID: 1, Active: true}, {ID: 2}}, map[uint32]string{1: bobSID, 2: aliceSID}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSessionAPI{sessions: tc.sessions, users: tc.users}
			tok, id, selected, err := f.api().acquire(aliceSID)
			if err != nil || !selected || id != tc.want {
				t.Fatalf("acquire = %d %t %v, want session %d", id, selected, err, tc.want)
			}
			primary := "primary " + string(rune('0'+tc.want))
			if tok.native.(*fakeHandle).name != primary || tok.native.(*fakeHandle).sid != aliceSID {
				t.Fatalf("token %+v", tok.native)
			}
			// Every other user's token and the source are closed; only the
			// prepared copy is owned.
			f.allClosedBut(t, primary)
		})
	}
}

func TestSessionSelectionNeverUsesAnotherUser(t *testing.T) {
	f := &fakeSessionAPI{
		sessions: []loggedOnSession{{ID: 1, Active: true}, {ID: 2}, {ID: 3}},
		users:    map[uint32]string{1: bobSID, 2: bobSID},
	}
	tok, _, selected, err := f.api().acquire(aliceSID)
	if tok != nil || selected || !errors.Is(err, errNoSignedInSession) || err.Error() != "no signed-in session of the account" {
		t.Fatalf("acquire = %v %t %v", tok, selected, err)
	}
	if len(f.duplicated) != 0 {
		t.Fatalf("duplicated %v", f.duplicated)
	}
	f.allClosedBut(t)
}

func TestSessionSelectionReportsAFailedQuery(t *testing.T) {
	// Whose session 2 is cannot be known, so the error is not "no session".
	f := &fakeSessionAPI{
		sessions: []loggedOnSession{{ID: 1, Active: true}, {ID: 2, Active: true}},
		users:    map[uint32]string{1: bobSID},
		queryErr: map[uint32]error{2: errors.New("access denied")},
	}
	_, _, selected, err := f.api().acquire(aliceSID)
	if selected || err == nil || err.Error() != "session 2: token: access denied" {
		t.Fatalf("acquire = %t %v", selected, err)
	}
}

func TestSessionTokenPreparation(t *testing.T) {
	t.Run("filtered administrator gets the full token", func(t *testing.T) {
		f := &fakeSessionAPI{sessions: []loggedOnSession{{ID: 2, Active: true}}, users: map[uint32]string{2: aliceSID}, filtered: map[uint32]bool{2: true}}
		tok, _, _, err := f.api().acquire(aliceSID)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(f.duplicated, []string{"linked 2"}) {
			t.Fatalf("duplicated %v, want only the linked token", f.duplicated)
		}
		f.allClosedBut(t, "primary 2")
		_ = tok.Close()
	})
	t.Run("linked token failure", func(t *testing.T) {
		f := &fakeSessionAPI{sessions: []loggedOnSession{{ID: 2, Active: true}}, users: map[uint32]string{2: aliceSID}, filtered: map[uint32]bool{2: true}, linkedErr: errors.New("no linked token")}
		tok, id, selected, err := f.api().acquire(aliceSID)
		if tok != nil || !selected || id != 2 || err == nil || err.Error() != "session 2: linked token: no linked token" {
			t.Fatalf("acquire = %v %d %t %v", tok, id, selected, err)
		}
		if len(f.duplicated) != 0 {
			t.Fatalf("the filtered token reached duplicate: %v", f.duplicated)
		}
		f.allClosedBut(t)
	})
	t.Run("unclosable token stays owned", func(t *testing.T) {
		f := &fakeSessionAPI{sessions: []loggedOnSession{{ID: 1, Active: true}, {ID: 2}}, users: map[uint32]string{1: bobSID, 2: aliceSID}, closeErr: map[string]error{"session 1": errors.New("close failed")}}
		tok, _, selected, err := f.api().acquire(aliceSID)
		if tok == nil || selected || err == nil {
			t.Fatalf("acquire = %v %t %v", tok, selected, err)
		}
		delete(f.closeErr, "session 1")
		if err := tok.Close(); err != nil || !slices.Contains(f.closed, "session 1") {
			t.Fatalf("retry close = %v, closed %v", err, f.closed)
		}
	})
}

// The sign-out exception asks only the token query and the SID; a query that
// cannot tell must not turn a failed session path into a failed start.
func TestSessionSignedIn(t *testing.T) {
	f := &fakeSessionAPI{users: map[uint32]string{1: aliceSID, 2: bobSID}, queryErr: map[uint32]error{4: errors.New("access denied")}}
	api := f.api()
	for id, want := range map[uint32]bool{1: true, 2: false, 3: false, 4: true} {
		if got := api.signedIn(id, aliceSID); got != want {
			t.Errorf("session %d: signedIn = %t, want %t", id, got, want)
		}
	}
	f.allClosedBut(t)
}

func TestOpenAccountLogons(t *testing.T) {
	s4uErr := errors.New("S4U logon for " + aliceSID + ": Negotiate S4U: domain unavailable")
	opened := &AccountSession{Logon: LogonS4U}
	for _, tc := range []struct {
		name        string
		trySession  bool
		session     func() (*AccountSession, error)
		s4u         error
		wantS4U     bool
		wantErr     string
		wantSession bool
	}{
		{"default is S4U only", false, nil, nil, true, "", false},
		{"session success", true, func() (*AccountSession, error) { return &AccountSession{Logon: LogonSession}, nil }, nil, false, "", true},
		{"no session", true, func() (*AccountSession, error) { return nil, errNoSignedInSession }, s4uErr, true,
			"no signed-in session of the account; S4U logon for " + aliceSID + ": Negotiate S4U: domain unavailable", false},
		{"failed session path", true, func() (*AccountSession, error) { return nil, errors.New("session 2: linked token: denied") }, s4uErr, true,
			"session 2: linked token: denied; S4U logon for " + aliceSID + ": Negotiate S4U: domain unavailable", false},
		{"falls back to S4U", true, func() (*AccountSession, error) { return nil, errNoSignedInSession }, nil, true, "", false},
		{"failed cleanup", true, func() (*AccountSession, error) {
			return &AccountSession{}, errors.New("session 2: close token: denied")
		}, nil, false, "session 2: close token: denied", true},
		{"signed out during the launch", true, func() (*AccountSession, error) { return nil, &sessionGoneError{ID: 2} }, nil, false, "session 2: signed out during the launch", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			s, err := openAccountLogons(tc.trySession, tc.session, func() (*AccountSession, error) {
				called = true
				if tc.s4u != nil {
					return nil, tc.s4u
				}
				return opened, nil
			})
			if called != tc.wantS4U {
				t.Fatalf("S4U called %t, want %t", called, tc.wantS4U)
			}
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != tc.wantErr {
				t.Fatalf("error %q, want %q", got, tc.wantErr)
			}
			if (s != nil && s != opened) != tc.wantSession {
				t.Fatalf("session %+v", s)
			}
		})
	}
}
