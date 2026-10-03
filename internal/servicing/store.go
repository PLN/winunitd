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
// a regular file that only trusted principals can change. Readers take no
// lock: a record is replaced atomically, so a reader sees one whole record.
func ReadFloor(path string) (*Floor, error) {
	dirs, present, err := openFloorDirs(path)
	if err != nil {
		return nil, err
	}
	defer dirs.Close()
	if !present {
		return nil, nil
	}
	return readHeld(path)
}

func readHeld(path string) (*Floor, error) {
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

// LockFileName is the floor's cross-process mutation lock, beside the
// record in the daemon directory. It is never removed: removing a lock file
// that others may have opened would let two changes hold it.
const LockFileName = "compat-floor.lock"

// DefaultLockWait bounds how long a floor change waits for another one.
const DefaultLockWait = 30 * time.Second

const lockPoll = 20 * time.Millisecond

// ErrNoDataDirectory reports that the data root or daemon directory does not
// exist yet: there is no floor, and none can be written.
var ErrNoDataDirectory = errors.New("the daemon data directory does not exist; install the product first")

// ErrFloorBusy reports that another floor change held the lock for longer
// than the wait.
var ErrFloorBusy = errors.New("another compatibility floor change is in progress")

// FloorChange is one floor mutation in progress. It holds the checked
// directory chain and the floor's exclusive cross-process lock from before
// it reads the current record until it has written or removed it, so no
// other change can read, classify or replace the record in between.
type FloorChange struct {
	path string
	dirs io.Closer
	lock io.Closer
}

// BeginFloorChange takes the floor's lock for path, waiting at most wait,
// and fails closed: an unsafe chain, a missing daemon directory
// (ErrNoDataDirectory) or a lock held too long (ErrFloorBusy) is an error.
func BeginFloorChange(path string, wait time.Duration) (*FloorChange, error) {
	dirs, present, err := openFloorDirs(path)
	if err != nil {
		return nil, err
	}
	if !present {
		_ = dirs.Close()
		return nil, ErrNoDataDirectory
	}
	lock, err := lockFloor(filepath.Join(filepath.Dir(path), LockFileName), wait)
	if err != nil {
		_ = dirs.Close()
		return nil, err
	}
	return &FloorChange{path: path, dirs: dirs, lock: lock}, nil
}

// Current reads the record under the lock.
func (c *FloorChange) Current() (*Floor, error) { return readHeld(c.path) }

// Write atomically replaces the record with a protected file.
func (c *FloorChange) Write(f *Floor) error {
	data, err := EncodeFloor(f)
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp-" + strconv.Itoa(os.Getpid())
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
		err := os.Rename(tmp, c.path)
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

// Remove deletes the record. An absent record is not an error.
func (c *FloorChange) Remove() error {
	err := os.Remove(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Close releases the lock and the directory chain. Later calls do nothing.
func (c *FloorChange) Close() error {
	if c.lock == nil {
		return nil
	}
	err := errors.Join(c.lock.Close(), c.dirs.Close())
	c.lock, c.dirs = nil, nil
	return err
}

// WriteFloor replaces the floor record at path under the floor's lock. The
// data root and daemon directory must already exist.
func WriteFloor(path string, f *Floor) error {
	if _, err := EncodeFloor(f); err != nil {
		return err
	}
	c, err := BeginFloorChange(path, DefaultLockWait)
	if err != nil {
		return err
	}
	return errors.Join(c.Write(f), c.Close())
}

// RemoveFloor deletes the floor record under the floor's lock. An absent
// record or data directory is not an error.
func RemoveFloor(path string) error {
	c, err := BeginFloorChange(path, DefaultLockWait)
	if errors.Is(err, ErrNoDataDirectory) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.Join(c.Remove(), c.Close())
}

// floorChain names the levels openFloorDirs checks for the record at path.
func floorChain(path string) (container, root, daemon string) {
	daemon = filepath.Dir(path)
	root = filepath.Dir(daemon)
	return filepath.Dir(root), root, daemon
}
