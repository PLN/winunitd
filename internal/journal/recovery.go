package journal

import (
	"fmt"
	"io"
	"os"
)

// Restore the JSON-lines boundary after an interrupted append. Preserve the
// existing bytes: readers can skip a damaged final line, or recover a complete
// JSON record whose final newline was lost. Never concatenate a new record to it.
func repairJournalTail(f *os.File) (size int64, repaired bool, err error) {
	st, err := f.Stat()
	if err != nil {
		return 0, false, err
	}
	size = st.Size()
	if size == 0 {
		return size, false, nil
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], size-1); err != nil {
		return 0, false, err
	}
	if last[0] == '\n' {
		return size, false, nil
	}
	n, err := f.Write([]byte{'\n'})
	if err == nil && n != 1 {
		err = io.ErrShortWrite
	}
	if err != nil {
		return 0, false, fmt.Errorf("repair interrupted journal boundary: %w", err)
	}
	return size + 1, true, nil
}
