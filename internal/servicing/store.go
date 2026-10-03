package servicing

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// FloorFileName is the floor record inside the machine data root's daemon
// directory. That tree is permanent across repair, upgrade and uninstall.
const FloorFileName = "compat-floor.json"

// FloorPath is the floor record for the data root baseDir.
func FloorPath(baseDir string) string {
	return filepath.Join(baseDir, "daemon", FloorFileName)
}

// The record's directory chain: the containing directory of the data root,
// the data root, and its daemon directory. Before a record is read, written
// or removed, and before its absence counts as "no floor", openFloorDirs
// opens the chain without following a reparse point, checks each level on
// its open handle and holds it until the operation ends:
//
//   - the containing directory exists, is owned by a trusted principal and
//     lets no other principal delete, rename or re-permission its entries;
//   - the data root and the daemon directory, when they exist, are owned by
//     a trusted principal and let no other principal write, delete or
//     re-permission them or anything in them, including through
//     inherit-only grants.
//
// Trusted principals are SYSTEM, Administrators, TrustedInstaller and the
// account of the calling process, which is SYSTEM for the manager and the
// package helper. A missing data root or daemon directory is a first install
// or bootstrap: no record can exist there, so there is no floor. An unsafe
// existing level is an error, so a record that another account could have
// removed or replaced is never read as absent.

// ReadFloor reads and validates the floor record at path. It returns nil and
// no error when the record is absent from a trusted chain. The record must be
// a regular file that only trusted principals can change.
func ReadFloor(path string) (*Floor, error) {
	dirs, present, err := openFloorDirs(path)
	if err != nil {
		return nil, err
	}
	defer dirs.Close()
	if !present {
		return nil, nil
	}
	f, err := openProtected(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFloorBytes+1))
	if err != nil {
		return nil, err
	}
	return DecodeFloor(data)
}

// WriteFloor atomically replaces the floor record at path with a protected
// file. The data root and daemon directory must already exist.
func WriteFloor(path string, f *Floor) error {
	data, err := EncodeFloor(f)
	if err != nil {
		return err
	}
	dirs, present, err := openFloorDirs(path)
	if err != nil {
		return err
	}
	defer dirs.Close()
	if !present {
		return errors.New("the daemon data directory does not exist; install the product first")
	}
	tmp := path + ".tmp-" + strconv.Itoa(os.Getpid())
	out, err := createProtected(tmp)
	if err != nil {
		return err
	}
	_, werr := out.Write(data)
	serr := out.Sync()
	cerr := out.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// A reader without delete sharing can briefly refuse the replacement.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := os.Rename(tmp, path)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			_ = os.Remove(tmp)
			return fmt.Errorf("replace floor record: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// RemoveFloor deletes the floor record. An absent record is not an error.
func RemoveFloor(path string) error {
	dirs, present, err := openFloorDirs(path)
	if err != nil {
		return err
	}
	defer dirs.Close()
	if !present {
		return nil
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// floorChain names the levels openFloorDirs checks for the record at path.
func floorChain(path string) (container, root, daemon string) {
	daemon = filepath.Dir(path)
	root = filepath.Dir(daemon)
	return filepath.Dir(root), root, daemon
}
