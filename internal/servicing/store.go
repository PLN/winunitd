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

// ReadFloor reads and validates the floor record at path. It returns nil and
// no error when the record is absent. The record must be a regular file that
// only SYSTEM and Administrators (on other systems: its owner) can change; a
// record anyone else could have lowered is not trusted.
func ReadFloor(path string) (*Floor, error) {
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
// file. The daemon directory must already exist.
func WriteFloor(path string, f *Floor) error {
	data, err := EncodeFloor(f)
	if err != nil {
		return err
	}
	if err := protectedParent(parentDir(path)); err != nil {
		return err
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

func parentDir(path string) string { return filepath.Dir(path) }

// RemoveFloor deletes the floor record. An absent record is not an error.
func RemoveFloor(path string) error {
	if err := protectedParent(parentDir(path)); err != nil {
		return err
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
