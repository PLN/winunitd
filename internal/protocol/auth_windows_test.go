//go:build windows

package protocol

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestPeerFromTokenNonAdmin(t *testing.T) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &tok); err != nil {
		t.Fatal(err)
	}
	defer tok.Close()

	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := createRestrictedToken(tok, []windows.SIDAndAttributes{{Sid: adminSID}})
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Close()

	p, err := peerFromToken(restricted, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.SID == "" {
		t.Fatal("SID must be set")
	}
	if p.Administrator {
		t.Fatalf("restricted token must not be Administrator: %+v", p)
	}
	if p.CanLinger() {
		t.Fatalf("non-admin peer must not linger: %+v", p)
	}
}

func TestPeerFromTokenOwnerMatch(t *testing.T) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &tok); err != nil {
		t.Fatal(err)
	}
	defer tok.Close()

	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	p, err := peerFromToken(tok, sid)
	if err != nil {
		t.Fatal(err)
	}
	if p.SID != sid {
		t.Fatalf("SID = %q, want %q", p.SID, sid)
	}
	if !p.Owner {
		t.Fatalf("matching owner SID must set Owner: %+v", p)
	}

	other, err := peerFromToken(tok, "S-1-5-21-1-2-3-1001")
	if err != nil {
		t.Fatal(err)
	}
	if other.Owner {
		t.Fatal("other SID must not be Owner")
	}
}

func TestDefaultAuthorizerNamedPipePeer(t *testing.T) {
	name := fmt.Sprintf(`\\.\pipe\winunitd-auth-%d-%d`, os.Getpid(), time.Now().UnixNano())
	lis, err := ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	type result struct {
		peer Peer
		err  error
	}
	got := make(chan result, 1)
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			got <- result{err: err}
			return
		}
		defer conn.Close()
		p, err := DefaultAuthorizer()(conn)
		got <- result{peer: p, err: err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DialPipe(ctx, name)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	var r result
	select {
	case r = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("accept/authorizer timed out")
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if r.peer.SID != sid {
		t.Fatalf("peer SID = %q, want %q", r.peer.SID, sid)
	}
	if r.peer.Administrator && !r.peer.CanLinger() {
		t.Fatal("Administrator must be able to linger")
	}
	if !r.peer.Administrator && r.peer.CanLinger() && !r.peer.LocalSystem {
		t.Fatalf("non-admin peer must not linger: %+v", r.peer)
	}
}

func TestUserAuthorizerMarksOwner(t *testing.T) {
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	sddl, err := UserPipeSDDL(sid)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf(`\\.\pipe\winunitd-user-auth-%d-%d`, os.Getpid(), time.Now().UnixNano())
	lis, err := ListenPipeSDDL(name, sddl)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	type result struct {
		peer Peer
		err  error
	}
	got := make(chan result, 1)
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			got <- result{err: err}
			return
		}
		defer conn.Close()
		p, err := UserAuthorizer(sid)(conn)
		got <- result{peer: p, err: err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DialPipe(ctx, name)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	var r result
	select {
	case r = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("accept/authorizer timed out")
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.peer.SID != sid {
		t.Fatalf("peer SID = %q, want %q", r.peer.SID, sid)
	}
	if !r.peer.Owner {
		t.Fatalf("user-pipe client must be Owner: %+v", r.peer)
	}
}

func TestNamedPipePeerPrefersImpersonation(t *testing.T) {
	name := fmt.Sprintf(`\\.\pipe\winunitd-imp-%d-%d`, os.Getpid(), time.Now().UnixNano())
	lis, err := ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	type result struct {
		peer Peer
		err  error
	}
	got := make(chan result, 1)
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			got <- result{err: err}
			return
		}
		defer conn.Close()
		p, err := authorizerWithLookups("", impersonatePeerPlatform, func(net.Conn, string) (Peer, error) {
			return Peer{SID: "S-1-5-21-9-9-9-9", Administrator: true}, nil
		})(conn)
		got <- result{peer: p, err: err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DialPipe(ctx, name)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	var r result
	select {
	case r = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("accept/authorizer timed out")
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if r.peer.SID != sid {
		t.Fatalf("impersonation peer SID = %q, want %q (PID fallback SID must not win)", r.peer.SID, sid)
	}
	if r.peer.SID == "S-1-5-21-9-9-9-9" {
		t.Fatal("PID fallback peer used while impersonation should succeed")
	}
}

func TestNamedPipePeerPIDFallback(t *testing.T) {
	name := fmt.Sprintf(`\\.\pipe\winunitd-pidfb-%d-%d`, os.Getpid(), time.Now().UnixNano())
	lis, err := ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	type result struct {
		peer Peer
		err  error
	}
	got := make(chan result, 1)
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			got <- result{err: err}
			return
		}
		defer conn.Close()
		p, err := authorizerWithLookups("", func(net.Conn, string) (Peer, error) {
			return Peer{}, fmt.Errorf("anonymous dial")
		}, clientProcessPeerPlatform)(conn)
		got <- result{peer: p, err: err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DialPipe(ctx, name)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	var r result
	select {
	case r = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("accept/authorizer timed out")
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if r.peer.SID != sid {
		t.Fatalf("PID fallback peer SID = %q, want %q", r.peer.SID, sid)
	}
}

func TestPeerFromTokenIdentificationImpersonation(t *testing.T) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &tok); err != nil {
		t.Fatal(err)
	}
	defer tok.Close()

	var idTok windows.Token
	if err := windows.DuplicateTokenEx(tok, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, nil, windows.SecurityIdentification, windows.TokenImpersonation, &idTok); err != nil {
		t.Fatal(err)
	}
	defer idTok.Close()

	p, err := peerFromToken(idTok, "")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if p.SID != sid {
		t.Fatalf("SID = %q, want %q", p.SID, sid)
	}
	if p.Administrator && !p.CanLinger() {
		t.Fatal("Administrator must be able to linger")
	}
	if !p.Administrator && p.CanLinger() && !p.LocalSystem {
		t.Fatalf("non-admin identification token must not linger: %+v", p)
	}
}
