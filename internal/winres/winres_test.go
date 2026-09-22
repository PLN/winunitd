package winres

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/PLN/winunitd/internal/version"
	"github.com/PLN/winunitd/internal/winevt"
)

func TestVersionInfoMatchesWindres(t *testing.T) {
	got, err := VersionInfo(referenceInfo())
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/versioninfo.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("VERSIONINFO len %d want %d", len(got), len(want))
	}
}

func TestMessageTableMatchesWindmc(t *testing.T) {
	got := MessageTable()
	want, err := os.ReadFile("testdata/messagetable.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("message table len %d want %d", len(got), len(want))
	}
}

func TestStampGoExecutable(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module tiny\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "tiny.exe")
	cmd := exec.Command("go", "build", "-trimpath", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if p, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, p)
	}
	info := referenceInfo()
	info.IncludeMessages = true
	if err := Stamp(out, info); err != nil {
		t.Fatal(err)
	}
	versionBlob, messageBlob := readResources(t, out)
	wantVersion, err := VersionInfo(info)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(versionBlob, wantVersion) {
		t.Fatalf("stamped VERSIONINFO len %d want %d", len(versionBlob), len(wantVersion))
	}
	if !bytes.Equal(messageBlob, MessageTable()) {
		t.Fatal("stamped message table mismatch")
	}
	text, ok := winevt.Render(winevt.IDStartupFailed, "listener unavailable")
	if !ok || text == "" || !bytes.Contains([]byte(text), []byte("winunitd startup failed")) {
		t.Fatalf("rendered startup event %q", text)
	}
	if _, err := VersionInfo(Info{Release: "nope"}); err == nil {
		t.Fatal("invalid release was accepted")
	}
	_ = version.InstallerVersion
}

func referenceInfo() Info {
	return Info{
		InternalName:     "winunitd",
		OriginalFilename: "winunitd.exe",
		FileDescription:  "WinUnit Manager",
		Release:          "0.1.0-alpha",
		Company:          "PLN",
		Product:          "WinUnit Manager",
		Copyright:        "Copyright (c) 2026 PLN",
	}
}

func readResources(t *testing.T, path string) (version, messages []byte) {
	t.Helper()
	f, err := pe.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	oh, ok := f.OptionalHeader.(*pe.OptionalHeader64)
	if !ok {
		t.Fatal("expected PE32+")
	}
	dir := oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_RESOURCE]
	if dir.VirtualAddress == 0 || dir.Size == 0 {
		t.Fatal("resource directory missing")
	}
	sec := sectionForRVA(t, f, dir.VirtualAddress)
	raw := mustRead(t, f, sec, dir.VirtualAddress, dir.Size)
	base := dir.VirtualAddress
	var walk func(off uint32, depth int, want uint32) uint32
	walk = func(off uint32, depth int, want uint32) uint32 {
		if depth == 3 {
			return off
		}
		if int(off)+16 > len(raw) {
			t.Fatalf("directory offset %d out of range", off)
		}
		ids := binary.LittleEndian.Uint16(raw[off+14:])
		ents := off + 16
		var next uint32
		found := false
		for i := 0; i < int(ids); i++ {
			e := ents + uint32(i*8)
			id := binary.LittleEndian.Uint32(raw[e:])
			ptr := binary.LittleEndian.Uint32(raw[e+4:])
			if depth == 0 && id != want {
				continue
			}
			found = true
			if ptr&0x80000000 != 0 {
				next = walk(ptr&^0x80000000, depth+1, want)
			} else {
				next = ptr
			}
			break
		}
		if !found {
			t.Fatalf("resource id %d not found at depth %d", want, depth)
		}
		return next
	}
	readData := func(typeID uint32) []byte {
		dataOff := walk(0, 0, typeID)
		if int(dataOff)+16 > len(raw) {
			t.Fatalf("data entry %d out of range", dataOff)
		}
		rva := binary.LittleEndian.Uint32(raw[dataOff:])
		size := binary.LittleEndian.Uint32(raw[dataOff+4:])
		dataSec := sectionForRVA(t, f, rva)
		return mustRead(t, f, dataSec, rva, size)
	}
	_ = base
	return readData(rtVersion), readData(rtMessageTable)
}

func sectionForRVA(t *testing.T, f *pe.File, rva uint32) *pe.Section {
	t.Helper()
	for _, s := range f.Sections {
		if rva >= s.VirtualAddress && rva < s.VirtualAddress+s.VirtualSize {
			return s
		}
	}
	t.Fatalf("no section for RVA %#x", rva)
	return nil
}

func mustRead(t *testing.T, _ *pe.File, s *pe.Section, rva, size uint32) []byte {
	t.Helper()
	data, err := s.Data()
	if err != nil {
		t.Fatal(err)
	}
	off := int(rva - s.VirtualAddress)
	if off < 0 || off+int(size) > len(data) {
		t.Fatalf("resource bytes at RVA %#x size %d outside section %s", rva, size, s.Name)
	}
	return data[off : off+int(size)]
}
