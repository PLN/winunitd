//go:build windows

package headless

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// probeTimeout bounds each network and file probe.
const probeTimeout = 10 * time.Second

// readFileOp reads a whole bounded file with ordinary CreateFileW and
// ReadFile: never a raw encrypted-backup read.
func readFileOp(op, path string) OpResult {
	r := OpResult{Op: op}
	f, err := os.Open(path)
	if err != nil {
		r.Win32 = win32Code(err)
		return r
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, MaxFileBytes))
	r.Bytes = int(n)
	if err != nil {
		r.Win32 = win32Code(err)
		return r
	}
	r.OK, r.SHA256 = true, hex.EncodeToString(h.Sum(nil))
	return r
}

func writeFileOp(op, path string, data []byte) OpResult {
	r := OpResult{Op: op}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		r.Win32 = win32Code(err)
		return r
	}
	n, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	r.Bytes = n
	if err := errors.Join(werr, serr, cerr); err != nil {
		r.Win32 = win32Code(err)
		return r
	}
	r.OK = true
	return r
}

func pathResult(probe string, op OpResult) PathResult {
	return PathResult{Probe: probe, OK: op.OK, Win32: op.Win32}
}

// ProbePaths runs a path set from the workload's own token.
func ProbePaths(set string, c *ProbeConfig, stateRoot, nonce string) []PathResult {
	var out []PathResult
	switch set {
	case PathsOwnRoots:
		out = append(out, pathResult("unit-fixture", readFileOp("read", c.UnitFile)))
		state := filepath.Join(stateRoot, "path-probe-"+nonce+".txt")
		out = append(out, pathResult("state-write", writeFileOp("write", state, []byte(nonce+"\n"))))
		read := readFileOp("read", state)
		want := sha256.Sum256([]byte(nonce + "\n"))
		if read.OK && read.SHA256 != hex.EncodeToString(want[:]) {
			read.OK, read.Win32 = false, uint32(windows.ERROR_INVALID_DATA)
		}
		out = append(out, pathResult("state-read", read))
		k, err := registry.OpenKey(registry.CURRENT_USER, `Software`, registry.READ)
		hkcu := PathResult{Probe: "hkcu", OK: err == nil}
		if err != nil {
			hkcu.Win32 = win32Code(err)
		} else {
			k.Close()
		}
		out = append(out, hkcu)
		tp, err := ProbeOwnToken()
		folders := PathResult{Probe: "known-folders", OK: err == nil && len(tp.KnownFolderErrors) == 0 && len(tp.KnownFolders) == 3}
		if err != nil {
			folders.Win32 = win32Code(err)
		}
		out = append(out, folders)
		_, err = os.ReadDir(c.PeerRoot)
		peer := PathResult{Probe: "peer-root", OK: err == nil}
		if err != nil {
			peer.Win32 = win32Code(err)
		}
		out = append(out, peer)
	case PathsMissingAndDenied:
		out = append(out, pathResult("absent", readFileOp("read", c.Absent)))
		out = append(out, pathResult("denied", readFileOp("read", c.Denied)))
	}
	return out
}

// ProbeSMB reaches the server's TCP 445, then reads the expected nonce file
// and writes a new one beside it with ordinary file calls: no user name,
// password, credential URI, impersonation or credential-store entry.
func ProbeSMB(t *SMBTarget, nonce string) SMBResult {
	r := SMBResult{ExpectSHA256: t.ExpectSHA256}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(t.Server, "445"), probeTimeout)
	if err != nil {
		r.ReachError = socketError(err)
		return r
	}
	_ = c.Close()
	r.Reachable = true
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Read = readFileOp("read", t.Path)
		r.Write = writeFileOp("write", filepath.Join(filepath.Dir(t.Path), "s4u-"+nonce+".txt"), []byte(nonce+"\n"))
	}()
	select {
	case <-done:
	case <-time.After(2 * probeTimeout):
		// The file calls cannot be cancelled; the workload's job bounds them.
		return SMBResult{Reachable: true, ExpectSHA256: t.ExpectSHA256,
			Read: OpResult{Op: "read", Win32: uint32(windows.WAIT_TIMEOUT)}, Write: OpResult{Op: "write", Win32: uint32(windows.WAIT_TIMEOUT)}}
	}
	return r
}

// ProbeEFS checks the volume's encryption support and the fixture's
// encrypted attribute, then reads the fixture and its plain sibling with
// ordinary reads.
func ProbeEFS(t *EFSTarget) EFSResult {
	r := EFSResult{ExpectSHA256: t.ExpectSHA256}
	volume := make([]uint16, windows.MAX_PATH+1)
	p, err := windows.UTF16PtrFromString(t.Path)
	if err == nil {
		err = windows.GetVolumePathName(p, &volume[0], uint32(len(volume)))
	}
	var flags uint32
	if err == nil {
		err = windows.GetVolumeInformation(&volume[0], nil, 0, nil, nil, &flags, nil, 0)
	}
	if err != nil {
		r.VolumeError = win32Code(err)
	}
	r.VolumeEncryption = err == nil && flags&windows.FILE_SUPPORTS_ENCRYPTION != 0
	if attrs, err := windows.GetFileAttributes(p); err == nil {
		r.Encrypted = attrs&windows.FILE_ATTRIBUTE_ENCRYPTED != 0
	}
	r.Read = readFileOp("read", t.Path)
	r.Plain = readFileOp("read", t.Plain)
	return r
}
