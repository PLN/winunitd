package journal

import "io"

// Attach consumes stdout and stderr for a unit invocation. M8 will persist
// the bytes; M5 only keeps the pipes from filling.
func Attach(unit string, stdout, stderr io.Reader) {
	go drain(unit, "stdout", stdout)
	go drain(unit, "stderr", stderr)
}

func drain(unit, stream string, r io.Reader) {
	_ = unit
	_ = stream
	if r == nil {
		return
	}
	_, _ = io.Copy(io.Discard, r)
}
