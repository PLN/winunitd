//go:build windows

package runtime

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procNtQueryObject    = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryObject")
	sentinelKernel32     = windows.NewLazySystemDLL("kernel32.dll")
	procCreateSemaphoreW = sentinelKernel32.NewProc("CreateSemaphoreW")
	procCreateJobObjectW = sentinelKernel32.NewProc("CreateJobObjectW")
)

// createNamed calls a kernel object constructor that returns a handle and
// reports an existing object of the same name through ERROR_ALREADY_EXISTS,
// which x/sys does not surface for these two.
func createNamed(proc *windows.LazyProc, args ...uintptr) (windows.Handle, error) {
	r, _, err := proc.Call(args...)
	h := windows.Handle(r)
	switch {
	case h == 0:
		return 0, err
	case errors.Is(err, windows.ERROR_ALREADY_EXISTS):
		return h, err
	}
	return h, nil
}

// OBJECT_INFORMATION_CLASS values.
const (
	objectNameInformation = 1
	objectTypeInformation = 2
)

// sectionNonceBytes is the size of a section sentinel and of the nonce read
// back from it.
const sectionNonceBytes = 64

// objectInformation is NtQueryObject's result for a handle and class.
func objectInformation(h windows.Handle, class uintptr) ([]byte, error) {
	buf := make([]byte, 512)
	for range 4 {
		var needed uint32
		r, _, _ := procNtQueryObject.Call(uintptr(h), class, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&needed)))
		switch status := windows.NTStatus(r); status {
		case windows.STATUS_SUCCESS:
			return buf, nil
		case windows.STATUS_INFO_LENGTH_MISMATCH, windows.STATUS_BUFFER_OVERFLOW, windows.STATUS_BUFFER_TOO_SMALL:
			buf = make([]byte, max(int(needed), 2*len(buf)))
		default:
			return nil, status
		}
	}
	return nil, errors.New("object information keeps growing")
}

// leadingString is the UNICODE_STRING at the start of an object
// information buffer: OBJECT_NAME_INFORMATION's name or
// OBJECT_TYPE_INFORMATION's type name.
func leadingString(buf []byte) string {
	return (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0])).String()
}

// ObjectIdentityOf reads the identity of the object a handle number names,
// asking about a sentinel kind. It first duplicates the handle into one this
// process holds, so every later query is of that one object even if the
// number is closed or reused meanwhile; a number that names no handle is
// absent. The type is read first, and the name and, for a section, the
// contents through a read-only view only for the expected type, so a pipe
// or other file handle is not name-queried; that the type query returns
// promptly is the observed behavior the native run must confirm. Any failed
// query leaves the identity unknown, with the query and its code.
func ObjectIdentityOf(h windows.Handle, kind string) ObjectIdentity {
	id := ObjectIdentity{Kind: kind, Status: ObjectUnknown}
	want, ok := sentinelTypes[kind]
	if !ok {
		id.Error = "kind"
		return id
	}
	self := windows.CurrentProcess()
	var held windows.Handle
	if err := windows.DuplicateHandle(self, h, self, &held, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		if errors.Is(err, windows.ERROR_INVALID_HANDLE) {
			return ObjectIdentity{Kind: kind, Status: ObjectAbsent}
		}
		id.Error = queryError("duplicate", err)
		return id
	}
	defer windows.CloseHandle(held)
	buf, err := objectInformation(held, objectTypeInformation)
	if err != nil {
		id.Error = queryError("type", err)
		return id
	}
	if id.Type = leadingString(buf); id.Type != want {
		id.Status = ObjectOtherType
		return id
	}
	if buf, err = objectInformation(held, objectNameInformation); err != nil {
		id.Error = queryError("name", err)
		return id
	}
	id.Name = leadingString(buf)
	if kind == SentinelSection {
		if id.Content, err = sectionContent(held); err != nil {
			id.Name, id.Error = "", queryError("view", err)
			return id
		}
	}
	id.Status = ObjectIdentified
	return id
}

// queryError names a failed query and its numeric code.
func queryError(op string, err error) string {
	var status windows.NTStatus
	var errno windows.Errno
	switch {
	case errors.As(err, &status):
		return fmt.Sprintf("%s %#x", op, uint32(status))
	case errors.As(err, &errno):
		return fmt.Sprintf("%s %d", op, uint32(errno))
	}
	return op
}

// sectionContent is the nonce at the start of a section, read through a
// read-only view.
func sectionContent(h windows.Handle) (string, error) {
	addr, err := windows.MapViewOfFile(h, windows.FILE_MAP_READ, 0, 0, sectionNonceBytes)
	if err != nil {
		return "", err
	}
	defer windows.UnmapViewOfFile(addr)
	view := unsafe.Slice(*(**byte)(unsafe.Pointer(&addr)), sectionNonceBytes)
	return strings.TrimRight(string(view), "\x00"), nil
}

// NewObjectSentinel creates an inheritable named sentinel of a kind with
// fresh nonces and returns its handle and identity as this process reads
// it. The caller owns the handle and closes it.
func NewObjectSentinel(kind string) (windows.Handle, ObjectIdentity, error) {
	name, err := windows.UTF16PtrFromString(SentinelName(kind, rand.Text()))
	if err != nil {
		return 0, ObjectIdentity{}, err
	}
	sa := &windows.SecurityAttributes{InheritHandle: 1}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	var h windows.Handle
	switch kind {
	case SentinelEvent:
		h, err = windows.CreateEvent(sa, 1, 0, name)
	case SentinelSection:
		h, err = windows.CreateFileMapping(windows.InvalidHandle, sa, windows.PAGE_READWRITE, 0, sectionNonceBytes, name)
	case SentinelMutex:
		h, err = windows.CreateMutex(sa, false, name)
	case SentinelSemaphore:
		h, err = createNamed(procCreateSemaphoreW, uintptr(unsafe.Pointer(sa)), 0, 1, uintptr(unsafe.Pointer(name)))
	case SentinelJob:
		h, err = createNamed(procCreateJobObjectW, uintptr(unsafe.Pointer(sa)), uintptr(unsafe.Pointer(name)))
	default:
		return 0, ObjectIdentity{}, fmt.Errorf("sentinel kind %q", kind)
	}
	if h != 0 && err != nil {
		// The name already existed: someone else's object, not a sentinel.
		_ = windows.CloseHandle(h)
	}
	if err != nil {
		return 0, ObjectIdentity{}, err
	}
	if kind == SentinelSection {
		if err := writeSectionNonce(h, "content-"+rand.Text()); err != nil {
			_ = windows.CloseHandle(h)
			return 0, ObjectIdentity{}, err
		}
	}
	id := ObjectIdentityOf(h, kind)
	if !id.complete() {
		_ = windows.CloseHandle(h)
		return 0, ObjectIdentity{}, fmt.Errorf("the %s sentinel has no complete identity", kind)
	}
	return h, id, nil
}

func writeSectionNonce(h windows.Handle, nonce string) error {
	addr, err := windows.MapViewOfFile(h, windows.FILE_MAP_WRITE, 0, 0, sectionNonceBytes)
	if err != nil {
		return err
	}
	view := unsafe.Slice(*(**byte)(unsafe.Pointer(&addr)), sectionNonceBytes)
	copy(view, nonce)
	return windows.UnmapViewOfFile(addr)
}
