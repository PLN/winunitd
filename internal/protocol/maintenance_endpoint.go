package protocol

import (
	"context"
	"encoding/json"
	"net"
	"time"
)

// ServeMaintenance owns capacity independent of ordinary control connections.
// The endpoint supports only administrative global quiescence, not normal work.
func ServeMaintenance(ctx context.Context, lis net.Listener, next Handler, auth Authorizer) error {
	if next == nil {
		return ErrFailed("nil maintenance handler")
	}
	h := HandlerFunc(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method != MethodMaintenance {
			return nil, ErrMethodNotFound(method)
		}
		return next.Handle(ctx, method, params)
	})
	return ServeWithLimits(ctx, lis, h, auth, ServerLimits{
		Connections: 8, Requests: 1, Stops: 4, Diagnostics: 1,
		ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second,
	})
}
