package manager

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

func TestDisableLingerHasReservedNativeAdmission(t *testing.T) {
	h, store := testLingerHost(t)
	if _, err := h.EnableLinger(testSIDA); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, maxNativeUserWork), make(chan struct{})
	var workers sync.WaitGroup
	h.cfg.QueryToken = func(uint32) (*runtime.UserToken, error) {
		entered <- struct{}{}
		<-release
		return nil, runtime.ErrNoUserToken
	}
	defer func() { close(release); workers.Wait() }()
	for i := 0; i < maxNativeUserWork; i++ {
		workers.Add(1)
		go func(id uint32) { defer workers.Done(); h.Logon(id) }(uint32(i + 10))
	}
	for i := 0; i < maxNativeUserWork; i++ {
		awaitNativeWork(t, entered)
	}
	lookupEntered, lookupRelease := make(chan struct{}), make(chan struct{})
	lookup := h.cfg.Lookup
	var once sync.Once
	unblock := func() { once.Do(func() { close(lookupRelease) }) }
	defer unblock()
	h.cfg.Lookup = func(user string) (runtime.UserInfo, error) {
		close(lookupEntered)
		<-lookupRelease
		return lookup(user)
	}
	control := &Control{Units: testManager(t, nil), Users: h}
	client := snapshotControlClient(t, control)
	disabled := make(chan error, 1)
	go func() { _, err := client.DisableLinger(context.Background(), testSIDA); disabled <- err }()
	select {
	case <-lookupEntered:
	case err := <-disabled:
		t.Fatal("disable-linger could not reserve native progress", err)
	}
	if h.NativeWorkCount() != maxNativeUserWork+1 {
		t.Fatal("reserved revocation was not tracked")
	}
	var busy *protocol.Error
	if _, err := h.DisableLinger(testSIDB); !errors.As(err, &busy) || busy.Code != protocol.CodeBusy {
		t.Fatalf("revocation limit escaped: %v", err)
	}
	if _, err := h.EnableLinger(testSIDB); !errors.As(err, &busy) || busy.Code != protocol.CodeBusy {
		t.Fatalf("enable borrowed revocation capacity: %v", err)
	}
	// All three background classes can also retain native work. The complete
	// eight-slot view must remain queryable without weakening its copy bound.
	for _, class := range []userWorkClass{userWorkReconcile, userWorkPolicy, userWorkLingerScan} {
		w, err := h.acceptUserWorkClass(class)
		if err != nil {
			t.Fatal(err)
		}
		defer h.finishNativeUserWork(w, nil)
	}
	snapshot := userSnapshotFromWire(t, snapshotControlClient(t, control))
	if snapshot.UserHost.NativeWork != 8 || snapshot.Machine.UserNativeWork != 8 {
		t.Fatal("full native budget was not coherently copied")
	}
	unblock()
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	if store.Has(testSIDA) || h.Lingering(testSIDA) || h.Alive(testSIDA) || h.NativeWorkCount() != maxNativeUserWork+3 {
		t.Fatal("disable did not revoke and clean up under saturation")
	}
}
