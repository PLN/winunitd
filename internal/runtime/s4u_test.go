package runtime

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestLogon32FallbackIsBatchNotNetwork(t *testing.T) {
	t.Parallel()
	if logon32LogonBatch != 4 {
		t.Fatalf("LOGON32_LOGON_BATCH = %d, want 4", logon32LogonBatch)
	}
	if logon32LogonNetwork != 3 {
		t.Fatalf("LOGON32_LOGON_NETWORK = %d, want 3", logon32LogonNetwork)
	}
	if logon32LogonBatch == logon32LogonNetwork {
		t.Fatal("URI fallback must not use NETWORK")
	}
	if !logonTypeCachesOutboundCreds(logon32LogonBatch) {
		t.Fatal("Batch must cache outbound creds")
	}
	if logonTypeCachesOutboundCreds(logon32LogonNetwork) {
		t.Fatal("Network must not be treated as caching outbound creds")
	}
}

func TestLogonTypeCachesOutboundCreds(t *testing.T) {
	t.Parallel()
	yes := []uint32{
		logonTypeInteractive, logonTypeBatch, logonTypeService,
		logonTypeUnlock, logonTypeNetworkCleartext, logonTypeNewCredentials,
		logonTypeRemoteInteractive, logonTypeCachedInteractive,
	}
	for _, lt := range yes {
		if !logonTypeCachesOutboundCreds(lt) {
			t.Fatalf("logon type %d should cache outbound creds", lt)
		}
	}
	no := []uint32{0, logonTypeNetwork, 6, 12}
	for _, lt := range no {
		if logonTypeCachesOutboundCreds(lt) {
			t.Fatalf("logon type %d must not cache outbound creds", lt)
		}
	}
}

func TestUseStoreURIFallbackRule(t *testing.T) {
	t.Parallel()
	uri := "credman://winunitd/linger/S-1-5-21-1"
	if !useStoreURIFallback(uri, false) {
		t.Fatal("URI present and S4U insufficient → try store URI")
	}
	if useStoreURIFallback(uri, true) {
		t.Fatal("do not replace S4U when it already has network creds")
	}
	if useStoreURIFallback("", false) {
		t.Fatal("no URI → do not run CredMan")
	}
	if useStoreURIFallback("  ", false) {
		t.Fatal("blank URI → do not run CredMan")
	}
	if useStoreURIFallback("", true) {
		t.Fatal("no URI even if probe is true")
	}
}

func TestBlobPasswordUTF16(t *testing.T) {
	t.Parallel()
	got, err := blobPasswordUTF16(utf16LE("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if string(utf16.Decode(got)) != "hunter2" {
		t.Fatalf("got %q", string(utf16.Decode(got)))
	}

	withNUL := utf16LE("hunter2")
	withNUL = append(withNUL, 0, 0)
	got, err = blobPasswordUTF16(withNUL)
	if err != nil {
		t.Fatal(err)
	}
	if string(utf16.Decode(got)) != "hunter2" {
		t.Fatalf("NUL-trimmed = %q", string(utf16.Decode(got)))
	}

	empty, err := blobPasswordUTF16(nil)
	if err != nil || empty != nil {
		t.Fatalf("empty blob: %v %v", empty, err)
	}
}

func TestBlobPasswordUTF16RejectsOddLength(t *testing.T) {
	t.Parallel()
	if _, err := blobPasswordUTF16([]byte("abc")); err == nil {
		t.Fatal("odd-length blob must not be accepted as UTF-16")
	}
}

func TestBlobPasswordDoesNotGuessEvenUTF8(t *testing.T) {
	t.Parallel()
	// "secret!!" is even-length UTF-8. The old even-length⇒UTF-16
	// heuristic would decode it as four UTF-16 units, silently
	// garbling if we also accepted UTF-8. We document UTF-16LE only
	// (cmdkey/PowerShell); this is that encoding of "secret!!", not UTF-8.
	raw := utf16LE("secret!!")
	got, err := blobPasswordUTF16(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(utf16.Decode(got)) != "secret!!" {
		t.Fatalf("got %q", string(utf16.Decode(got)))
	}
	// The UTF-8 bytes of an even-length ASCII string are not the same
	// as UTF-16LE, so treating UTF-8 as UTF-16 would not yield the text.
	utf8Even := []byte("secret!!") // 8 bytes
	if bytes.Equal(utf8Even, raw) {
		t.Fatal("test setup: UTF-8 and UTF-16LE of secret!! must differ")
	}
	garbled, err := blobPasswordUTF16(utf8Even)
	if err != nil {
		t.Fatal(err)
	}
	if string(utf16.Decode(garbled)) == "secret!!" {
		t.Fatal("UTF-8 even-length must not silently decode as the same text")
	}
}

func TestZeroUTF16ClearsBuffer(t *testing.T) {
	t.Parallel()
	u := utf16.Encode([]rune("hunter2"))
	zeroUTF16(u)
	for i, c := range u {
		if c != 0 {
			t.Fatalf("u[%d] = %d after zero", i, c)
		}
	}
}

func TestZeroBytesClearsBuffer(t *testing.T) {
	t.Parallel()
	b := []byte("hunter2")
	zeroBytes(b)
	for i, c := range b {
		if c != 0 {
			t.Fatalf("b[%d] = %d after zero", i, c)
		}
	}
}

func TestS4UNames(t *testing.T) {
	t.Parallel()
	upn, realm := s4uNames(LingerRecord{Name: `TEST\alice`})
	if upn != "alice" || realm != "TEST" {
		t.Fatalf("DOMAIN\\user = %q %q", upn, realm)
	}
	upn, realm = s4uNames(LingerRecord{Name: "alice"})
	if upn != "alice" || realm != "" {
		t.Fatalf("bare = %q %q", upn, realm)
	}
	upn, realm = s4uNames(LingerRecord{SID: "S-1-5-21-1-2-3-1001"})
	if upn == "" && realm == "" {
		// Linux lookup returns SID-only UserInfo (no username). Empty is OK.
		return
	}
}

func TestSplitUserDomain(t *testing.T) {
	t.Parallel()
	u, d := splitUserDomain(`TEST\alice`)
	if u != "alice" || d != "TEST" {
		t.Fatalf("got %q %q", u, d)
	}
	u, d = splitUserDomain("alice@example.com")
	if u != "alice" || d != "example.com" {
		t.Fatalf("UPN = %q %q", u, d)
	}
	u, d = splitUserDomain("alice")
	if u != "alice" || d != "" {
		t.Fatalf("bare = %q %q", u, d)
	}
}

func TestFailLingerMapsWithoutPassword(t *testing.T) {
	t.Parallel()
	err := failLinger("S-1-5-21-1-2-3-1001", errors.New("LsaRegisterLogonProcess: access denied"))
	if !errors.Is(err, ErrNoLingerToken) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "S-1-5-21-1-2-3-1001") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "password") {
		t.Fatalf("must not mention passwords: %v", err)
	}
}

func TestLingerTokenPathConstants(t *testing.T) {
	t.Parallel()
	if LingerTokenPathS4U != "s4u" || LingerTokenPathStoreURI != "store-uri" {
		t.Fatalf("paths = %q %q", LingerTokenPathS4U, LingerTokenPathStoreURI)
	}
}

func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(out[2*i:], c)
	}
	return out
}
