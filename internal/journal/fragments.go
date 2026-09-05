package journal

import (
	"bufio"
	"io"
	"strings"
	"unicode/utf8"
)

// MaxCaptureFragment bounds the UTF-8 message bytes retained for a single
// stdout/stderr fragment. JSON encoding can expand the stored record.
const MaxCaptureFragment = 64 << 10

// captureFragments preserves UTF-8 rune boundaries and emits overlong lines
// before EOF. Invalid UTF-8 becomes replacement runes, as with JSON strings.
// partial means no newline terminates this fragment; continuation means this
// fragment follows another fragment of the same logical line on this stream.
func captureFragments(r io.Reader, emit func(string, bool, bool)) {
	br := bufio.NewReader(r)
	buf := make([]byte, 0, MaxCaptureFragment)
	continuation := false
	for {
		ch, _, err := br.ReadRune()
		if err != nil {
			if len(buf) > 0 {
				emit(string(buf), continuation, true)
			}
			return
		}
		if ch == '\n' {
			msg := strings.TrimRight(string(buf), "\r")
			if msg != "" || continuation {
				emit(msg, continuation, false)
			}
			buf = buf[:0]
			continuation = false
			continue
		}
		if len(buf)+utf8.RuneLen(ch) > MaxCaptureFragment {
			emit(string(buf), continuation, true)
			buf = buf[:0]
			continuation = true
		}
		buf = utf8.AppendRune(buf, ch)
	}
}
