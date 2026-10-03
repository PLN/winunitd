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

var procNtQueryObject = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryObject")

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

// ObjectIdentityOf reads the identity of the object a handle refers to,
// asking about a sentinel kind. The type is read first, which cannot block;
// the name and contents are read only for the expected type, so a pipe or
// other file handle at a reused number is never name-queried.
func ObjectIdentityOf(h windows.Handle, kind string) ObjectIdentity {
	id := ObjectIdentity{Kind: kind}
	want, ok := sentinelTypes[kind]
	if !ok {
		return id
	}
	buf, err := objectInformation(h, objectTypeInformation)
	if err != nil {
		return id
	}
	if id.Type = leadingString(buf); id.Type != want {
		return id
	}
	if buf, err := objectInformation(h, objectNameInformation); err == nil {
		id.Name = leadingString(buf)
	}
	if kind == SentinelSection {
		id.Content = sectionContent(h)
	}
	return id
}

// sectionContent is the nonce at the start of a section, read through a
// read-only view, or "" when no view can be mapped.
func sectionContent(h windows.Handle) string {
	addr, err := windows.MapViewOfFile(h, windows.FILE_MAP_READ, 0, 0, sectionNonceBytes)
	if err != nil {
		return ""
	}
	defer windows.UnmapViewOfFile(addr)
	view := unsafe.Slice(*(**byte)(unsafe.Pointer(&addr)), sectionNonceBytes)
	return strings.TrimRight(string(view), "\x00")
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
