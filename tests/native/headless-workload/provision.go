package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

// maxUnitBytes bounds a staged unit file.
const maxUnitBytes = 64 << 10

var unitName = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}\.service$`)

// Provisioned lists what a provisioning role wrote.
type Provisioned struct {
	Files []string `json:"files"`
}

func provision(role string, args []string, stdout, stderr io.Writer) int {
	var out Provisioned
	var err error
	switch role {
	case "provision-linger":
		out, err = runProvisionLinger(args)
	default:
		out, err = runProvisionUnit(args)
	}
	if err != nil {
		fmt.Fprintln(stderr, "headless-workload:", err)
		var usage *usageError
		if errors.As(err, &usage) {
			return 2
		}
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(out)
	return 0
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func flags(role string) *flag.FlagSet {
	fs := flag.NewFlagSet(role, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return &usageError{err.Error()}
	}
	if fs.NArg() != 0 {
		return &usageError{fmt.Sprintf("unexpected argument %q", fs.Arg(0))}
	}
	return nil
}

func absolute(name, p string) error {
	if p == "" || !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return &usageError{"--" + name + " must be a clean absolute path"}
	}
	return nil
}

// runProvisionLinger writes the explicit administrator grant for one
// account, with no credential URI, through the product's linger store, and
// reads it back with the same store. The installed broker must be stopped:
// a running broker would launch the account at once and could create its
// profile before the cold boot the grant is for.
func runProvisionLinger(args []string) (Provisioned, error) {
	fs := flags("provision-linger")
	base := fs.String("system-base", "", "")
	sid := fs.String("sid", "", "")
	name := fs.String("name", "", "")
	if err := parse(fs, args); err != nil {
		return Provisioned{}, err
	}
	if err := absolute("system-base", *base); err != nil {
		return Provisioned{}, err
	}
	if !protocol.ValidSID(*sid) || *sid == "S-1-5-18" {
		return Provisioned{}, &usageError{"--sid must be a user account SID"}
	}
	if err := brokerStopped(); err != nil {
		return Provisioned{}, err
	}
	dir := filepath.Join(*base, "linger")
	store := manager.OpenLingerStore(dir)
	rec := runtime.LingerRecord{SID: *sid, Name: *name}
	if err := store.Put(rec); err != nil {
		return Provisioned{}, err
	}
	got, err := store.Get(*sid)
	if err != nil {
		return Provisioned{}, err
	}
	if got != rec {
		return Provisioned{}, errors.New("the stored grant differs from the one written")
	}
	return Provisioned{Files: []string{filepath.Join(dir, *sid)}}, nil
}

// runProvisionUnit validates a unit with the product parser and writes its
// enable links with Manager.Enable on a scratch base, then copies exactly
// the unit file and those links into the target base: a Default-profile
// template or an existing profile's user base. Nothing else is created
// there, so the target's first boot is the manager's first.
func runProvisionUnit(args []string) (Provisioned, error) {
	fs := flags("provision-unit")
	unitFile := fs.String("unit-file", "", "")
	target := fs.String("target-base", "", "")
	if err := parse(fs, args); err != nil {
		return Provisioned{}, err
	}
	if err := errors.Join(absolute("unit-file", *unitFile), absolute("target-base", *target)); err != nil {
		return Provisioned{}, err
	}
	name := filepath.Base(*unitFile)
	if !unitName.MatchString(name) {
		return Provisioned{}, &usageError{"the unit file must be a .service unit"}
	}
	return stageUnit(*unitFile, *target)
}

func stageUnit(unitFile, target string) (Provisioned, error) {
	name := filepath.Base(unitFile)
	data, err := readBounded(unitFile, maxUnitBytes)
	if err != nil {
		return Provisioned{}, err
	}
	scratch, err := os.MkdirTemp("", "headless-provision-")
	if err != nil {
		return Provisioned{}, err
	}
	defer os.RemoveAll(scratch)
	if err := os.MkdirAll(filepath.Join(scratch, "units"), 0o755); err != nil {
		return Provisioned{}, err
	}
	if err := os.WriteFile(filepath.Join(scratch, "units", name), data, 0o644); err != nil {
		return Provisioned{}, err
	}
	m, err := manager.New(manager.Config{BaseDir: scratch, UserScope: true})
	if err != nil {
		return Provisioned{}, err
	}
	rel, err := m.Reload()
	if err == nil && len(rel.Errors) > 0 {
		err = fmt.Errorf("the unit does not load: %v", rel.Errors)
	}
	var res *protocol.EnableResult
	if err == nil {
		res, err = m.Enable(name)
	}
	m.Close()
	if err != nil {
		return Provisioned{}, err
	}
	files := []string{filepath.Join("units", name)}
	for _, t := range res.Targets {
		files = append(files, filepath.Join("enabled", t, name))
	}
	var out Provisioned
	for _, rel := range files {
		content, err := readBounded(filepath.Join(scratch, rel), maxUnitBytes)
		if err != nil {
			return out, err
		}
		dst := filepath.Join(target, rel)
		if err := placeFile(dst, content); err != nil {
			return out, err
		}
		out.Files = append(out.Files, dst)
	}
	return out, nil
}

// placeFile writes content at dst, accepting an identical existing file and
// refusing to replace a different one.
func placeFile(dst string, content []byte) error {
	if existing, err := readBounded(dst, maxUnitBytes); err == nil {
		if !bytes.Equal(existing, content) {
			return fmt.Errorf("%s already holds different content", filepath.Base(dst))
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(content)
	return errors.Join(werr, f.Sync(), f.Close())
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), limit)
	}
	return data, nil
}
