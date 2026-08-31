package journal

import "io"

// drain consumes r so a child pipe cannot fill.
func drain(r io.Reader) {
	if r == nil {
		return
	}
	_, _ = io.Copy(io.Discard, r)
}
