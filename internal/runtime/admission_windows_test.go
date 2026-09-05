package runtime

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestAdmissionDirectoryPresenceAndReparse(t *testing.T) {
	root := t.TempDir()
	probe := func(dir string) (bool, error) {
		var handles []admissionProbeHandle
		present, err := probeUnitDirectory(dir, &handles)
		for i := len(handles) - 1; i >= 0; i-- {
			if e := handles[i].close(); e != nil {
				t.Fatal(e)
			}
		}
		return present, err
	}
	if got, err := probe(filepath.Join(root, "missing")); got || err != nil {
		t.Fatalf("missing: %v %v", got, err)
	}
	if got, err := probe(root); got || err != nil {
		t.Fatalf("empty: %v %v", got, err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory.service"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("notes"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := probe(root); got || err != nil {
		t.Fatalf("unrelated entries: %v %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, "worker.service"), []byte("invalid unit contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := probe(root); !got || err != nil {
		t.Fatalf("presence: %v %v", got, err)
	}
	t.Run("reparse", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "units")
		makeAdmissionJunction(t, link, root)
		if got, err := probe(link); got || err == nil {
			t.Fatalf("reparse directory admitted: %v %v", got, err)
		}
	})
}

func TestAdmissionNativeProbeIdentityAndDeadlineOwnership(t *testing.T) {
	user := testUserToken(t)
	base := t.TempDir()
	dir := filepath.Join(base, "winunitd", "units")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "worker.service"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	resolve := func(tok windows.Token) (string, error) {
		var thread windows.Token
		if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &thread); err != nil {
			return "", err
		}
		defer thread.Close()
		a, err := thread.GetTokenUser()
		if err != nil {
			return "", err
		}
		b, err := tok.GetTokenUser()
		if err != nil {
			return "", err
		}
		if !a.User.Sid.Equals(b.User.Sid) {
			return "", fmt.Errorf("probe did not impersonate its supplied token")
		}
		return base, nil
	}
	if present, err := userUnitFilesPresent(user, resolve, time.Second); !present || err != nil {
		t.Fatalf("native identity probe: %v %v", present, err)
	}
	for len(admissionProbeSlots) != 0 {
		time.Sleep(time.Millisecond)
	}
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	verified := make(chan error, 4)
	blocked := func(tok windows.Token) (string, error) {
		entered <- struct{}{}
		<-release
		path, err := resolve(tok)
		verified <- err
		return path, err
	}
	for i := 0; i < 4; i++ {
		if present, err := userUnitFilesPresent(user, blocked, 50*time.Millisecond); present || err == nil {
			t.Fatal("blocked probe did not time out")
		}
		<-entered
	}
	if present, err := userUnitFilesPresent(user, resolve, time.Second); present || err == nil {
		t.Fatal("probe cap admitted a fifth worker")
	}
	if err := user.Close(); err != nil {
		t.Fatal(err)
	}
	unblock()
	for i := 0; i < 4; i++ {
		if err := <-verified; err != nil {
			t.Fatal("timed-out worker lost its duplicated token", err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for len(admissionProbeSlots) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("probe workers did not release admission")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAdmissionDirectoryPinRejectsMutation(t *testing.T) {
	dir := t.TempDir()
	var handles []admissionProbeHandle
	defer func() {
		for _, h := range handles {
			if err := h.close(); err != nil {
				t.Error(err)
			}
		}
	}()
	if _, err := probeUnitDirectory(dir, &handles); err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, access := range []uint32{windows.GENERIC_WRITE, windows.DELETE} {
		h, err := windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err == nil {
			windows.CloseHandle(h)
			t.Fatal("directory pin allowed a mutation handle")
		}
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			t.Fatalf("mutation was not blocked by the pin: %v", err)
		}
	}
}

func makeAdmissionJunction(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Mkdir(link, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Error(err)
		}
	})
	p, err := windows.UTF16PtrFromString(link)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	sub, err := windows.UTF16FromString(`\??\` + target)
	if err != nil {
		t.Fatal(err)
	}
	print, err := windows.UTF16FromString(target)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16+2*(len(sub)+len(print)))
	binary.LittleEndian.PutUint32(buf, windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(buf[4:], uint16(len(buf)-8))
	binary.LittleEndian.PutUint16(buf[10:], uint16(2*(len(sub)-1)))
	binary.LittleEndian.PutUint16(buf[12:], uint16(2*len(sub)))
	binary.LittleEndian.PutUint16(buf[14:], uint16(2*(len(print)-1)))
	for i, v := range append(sub, print...) {
		binary.LittleEndian.PutUint16(buf[16+i*2:], v)
	}
	var returned uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, (*byte)(unsafe.Pointer(&buf[0])), uint32(len(buf)), nil, 0, &returned, nil); err != nil {
		t.Fatal(err)
	}
}
