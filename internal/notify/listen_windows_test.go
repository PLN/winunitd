//go:build windows

package notify

import (
	"context"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"golang.org/x/sys/windows"
)

func TestNotifyPipeSDDLParses(t *testing.T) {
	sid, err := protocol.CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	sddl := PipeSDDL(sid)
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("dacl: %v", err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("DACL should be protected (D:P)")
	}
}

func TestListenNamedPipeAcceptsOwner(t *testing.T) {
	sid, err := protocol.CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	lis, err := Listen("p4acl.service", sid)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	if got := lis.Addr(); got != PipeName("p4acl.service") {
		t.Fatalf("addr = %q", got)
	}

	acceptErr := make(chan error, 1)
	go func() {
		c, err := lis.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		_ = c.Close()
		acceptErr <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, lis.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	select {
	case err := <-acceptErr:
		_ = conn.Close()
		if err != nil {
			t.Fatalf("accept: %v", err)
		}
	case <-ctx.Done():
		_ = conn.Close()
		t.Fatal(ctx.Err())
	}
}
