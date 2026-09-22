package winres

import (
	"encoding/binary"
	"unicode/utf16"

	"github.com/PLN/winunitd/internal/winevt"
)

// MessageTable builds a Unicode MESSAGETABLE resource for the winevt templates.
func MessageTable() []byte {
	templates := winevt.Templates()
	var entries []byte
	for _, t := range templates {
		entries = append(entries, messageEntry(t.Text)...)
	}
	buf := make([]byte, 16+len(entries))
	binary.LittleEndian.PutUint32(buf[0:], 1)
	binary.LittleEndian.PutUint32(buf[4:], templates[0].ID)
	binary.LittleEndian.PutUint32(buf[8:], templates[len(templates)-1].ID)
	binary.LittleEndian.PutUint32(buf[12:], 16)
	copy(buf[16:], entries)
	return buf
}

func messageEntry(text string) []byte {
	u := utf16.Encode([]rune(text + "\x00"))
	raw := make([]byte, len(u)*2)
	for i, c := range u {
		raw[i*2] = byte(c)
		raw[i*2+1] = byte(c >> 8)
	}
	length := 4 + len(raw)
	pad := (4 - length%4) % 4
	length += pad
	out := make([]byte, length)
	binary.LittleEndian.PutUint16(out[0:], uint16(length))
	binary.LittleEndian.PutUint16(out[2:], 1)
	copy(out[4:], raw)
	return out
}
