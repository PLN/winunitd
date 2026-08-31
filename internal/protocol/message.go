package protocol

import "encoding/json"

// Request is one control RPC call.
type Request struct {
	Protocol string          `json:"protocol"`
	Version  int             `json:"version"`
	ID       uint64          `json:"id"`
	Method   string          `json:"method"`
	Params   json.RawMessage `json:"params,omitempty"`
}

// Response is one control RPC reply. Result and Error are mutually exclusive.
type Response struct {
	Protocol string          `json:"protocol"`
	Version  int             `json:"version"`
	ID       uint64          `json:"id"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    *Error          `json:"error,omitempty"`
}

func newResponse(id uint64, result any, err error) *Response {
	resp := &Response{
		Protocol: Name,
		Version:  Version,
		ID:       id,
	}
	if err != nil {
		resp.Error = asError(err)
		return resp
	}
	if result == nil {
		result = struct{}{}
	}
	raw, jerr := json.Marshal(result)
	if jerr != nil {
		resp.Error = ErrFailed("encode result: " + jerr.Error())
		return resp
	}
	resp.Result = raw
	return resp
}
