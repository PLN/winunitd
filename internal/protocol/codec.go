package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const maxMessageBytes = 1 << 20 // 1 MiB

func encodeMessage(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data)+1 > maxMessageBytes {
		return fmt.Errorf("message too large")
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

func decodeMessage(r *bufio.Reader, v any) error {
	line, err := readLineLimited(r, maxMessageBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(line, v); err != nil {
		return err
	}
	return nil
}

func readLineLimited(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		part, err := r.ReadSlice('\n')
		if err != nil && err != bufio.ErrBufferFull {
			if err == io.EOF && len(buf)+len(part) > 0 {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if len(buf)+len(part) > max {
			return nil, fmt.Errorf("message too large")
		}
		buf = append(buf, part...)
		if err == nil {
			return bytes.TrimSuffix(buf, []byte{'\n'}), nil
		}
	}
}

// DecodeParams unmarshals RPC params. Empty or null params leave dst unchanged.
func DecodeParams(raw json.RawMessage, dst any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return ErrInvalidParams(err.Error())
	}
	return nil
}
