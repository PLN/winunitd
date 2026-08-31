package manager

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/PLN/winunitd/internal/protocol"
)

// Control is the system manager handler: unit verbs plus linger admin
// verbs. User managers use Manager directly (linger methods stay on the
// system pipe).
type Control struct {
	Units *Manager
	Users *UserHost
}

// Handle implements protocol.Handler.
func (c *Control) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if c == nil || c.Units == nil {
		return nil, protocol.ErrFailed("nil handler")
	}
	switch method {
	case protocol.MethodEnableLinger:
		var p protocol.LingerParams
		if err := protocol.DecodeParams(params, &p); err != nil {
			return nil, err
		}
		return c.enableLinger(p.User)
	case protocol.MethodDisableLinger:
		var p protocol.LingerParams
		if err := protocol.DecodeParams(params, &p); err != nil {
			return nil, err
		}
		return c.disableLinger(p.User)
	case protocol.MethodStatus:
		res, err := c.Units.Handle(ctx, method, params)
		if err != nil {
			return res, err
		}
		sr, ok := res.(*protocol.StatusResult)
		if ok && sr != nil && sr.Machine != nil && c.Users != nil {
			sr.Machine.UserManagers = len(c.Users.Running())
			sr.Machine.Lingering = c.Users.LingerCount()
		}
		return res, nil
	default:
		return c.Units.Handle(ctx, method, params)
	}
}

func (c *Control) enableLinger(user string) (*protocol.LingerResult, error) {
	if c.Users == nil {
		return nil, protocol.ErrFailed("user host is not configured")
	}
	user = strings.TrimSpace(user)
	if user == "" {
		return nil, protocol.ErrInvalidParams("user name required")
	}
	return c.Users.EnableLinger(user)
}

func (c *Control) disableLinger(user string) (*protocol.LingerResult, error) {
	if c.Users == nil {
		return nil, protocol.ErrFailed("user host is not configured")
	}
	user = strings.TrimSpace(user)
	if user == "" {
		return nil, protocol.ErrInvalidParams("user name required")
	}
	return c.Users.DisableLinger(user)
}
