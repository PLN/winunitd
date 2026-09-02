package journal

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitFileNameReservedDevices(t *testing.T) {
	t.Parallel()
	units := []string{
		"con.service",
		"prn.service",
		"aux.service",
		"nul.service",
		"com1.service",
		"com9.service",
		"lpt1.service",
		"lpt9.service",
		"CON.SERVICE",
		"Com1.Service",
		"con",
		"nul.timer",
	}
	seen := map[string]string{}
	for _, u := range units {
		fn := unitFileName(u)
		base := filepath.Base(fn)
		stem := strings.TrimSuffix(base, ".log")
		first := stem
		if i := strings.IndexByte(stem, '.'); i >= 0 {
			first = stem[:i]
		}
		if reservedDeviceName(first) {
			t.Errorf("unitFileName(%q) = %q: first component %q is reserved", u, fn, first)
		}
		if reservedDeviceName(stem) {
			t.Errorf("unitFileName(%q) = %q: basename %q is reserved", u, fn, stem)
		}
		if strings.HasSuffix(stem, ".") || strings.HasSuffix(stem, " ") {
			t.Errorf("unitFileName(%q) = %q: trailing dot/space", u, fn)
		}
		dec, ok := decodeUnitFileName(fn)
		if !ok {
			t.Errorf("unitFileName(%q) = %q: not reversible", u, fn)
			continue
		}
		if dec != canonicalUnit(u) {
			t.Errorf("decode(%q) = %q, want %q", fn, dec, canonicalUnit(u))
		}
		if other, ok := seen[fn]; ok && canonicalUnit(other) != canonicalUnit(u) {
			t.Errorf("collision %q vs %q -> %q", u, other, fn)
		}
		seen[fn] = u
	}
	if unitFileName("con.service") == unitFileName("_con.service") {
		t.Fatal("con.service must not collide with _con.service")
	}
	if unitFileName("foo.service") != "foo.service.log" {
		t.Fatalf("ordinary name changed: %q", unitFileName("foo.service"))
	}
}

func TestUnitFileNameTrailingDotSpace(t *testing.T) {
	t.Parallel()
	for _, u := range []string{"foo.service.", "foo.", "con.service."} {
		fn := unitFileName(u)
		stem := strings.TrimSuffix(fn, ".log")
		if strings.HasSuffix(stem, ".") || strings.HasSuffix(stem, " ") {
			t.Errorf("unitFileName(%q) = %q has trailing dot/space", u, fn)
		}
		first := stem
		if i := strings.IndexByte(stem, '.'); i >= 0 {
			first = stem[:i]
		}
		if reservedDeviceName(first) {
			t.Errorf("unitFileName(%q) = %q reserved stem %q", u, fn, first)
		}
		dec, ok := decodeUnitFileName(fn)
		if !ok || dec != canonicalUnit(u) {
			t.Errorf("decode(%q) = %q ok=%v, want %q", fn, dec, ok, canonicalUnit(u))
		}
	}
}
