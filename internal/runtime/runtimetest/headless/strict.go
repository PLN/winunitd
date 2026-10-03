package headless

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// decodeStrict decodes exactly one JSON value with no unknown fields and
// nothing but whitespace after it. Decoder.More alone accepts a trailing
// closing delimiter, so the next value must be io.EOF.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra json.RawMessage
	switch err := dec.Decode(&extra); {
	case err == io.EOF:
		return nil
	case err == nil:
		return errors.New("trailing JSON value")
	default:
		return fmt.Errorf("trailing data: %w", err)
	}
}
