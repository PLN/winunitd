package manager

import (
	"context"
	"errors"
	"fmt"

	"github.com/PLN/winunitd/internal/protocol"
)

// Compatible explicit starts share one admitted plan. Trigger activations and
// restarts retain their own semantics and do not join this flight.
type startFlight struct {
	operationContext context.Context
	id               string
	record           *unitRuntime
	stopEpoch        uint64
	revision         string
	done             chan struct{}
	result           *protocol.UnitResult
	err              error
}

func copyOperationReply(result *protocol.UnitResult, err error) (*protocol.UnitResult, error) {
	if result != nil {
		value := *result
		result = &value
	}
	var pe *protocol.Error
	if errors.As(err, &pe) {
		value := *pe
		err = &value
	}
	return result, err
}

func (f *startFlight) wait(ctx context.Context) (*protocol.UnitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var operationDone <-chan struct{}
	if f.operationContext != nil {
		operationDone = f.operationContext.Done()
	}
	select {
	case <-f.done:
		return copyOperationReply(f.result, f.err)
	default:
	}
	select {
	case <-operationDone:
		select {
		case <-f.done:
			return copyOperationReply(f.result, f.err)
		default:
		}
		return nil, &protocol.Error{Code: protocol.CodeFailed, Message: fmt.Sprintf("operation canceled; cleanup may remain: %v", context.Cause(f.operationContext)), OperationID: f.id}
	case <-f.done:
		return copyOperationReply(f.result, f.err)
	case <-ctx.Done():
		return nil, &protocol.Error{Code: protocol.CodeFailed, Message: fmt.Sprintf("operation wait: %v", ctx.Err()), OperationID: f.id}
	}
}

func (m *Manager) finishStartFlight(name string, flight *startFlight, result *protocol.UnitResult, err error) {
	if flight == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	flight.result, flight.err = copyOperationReply(result, err)
	if m.startFlights[name] == flight {
		delete(m.startFlights, name)
	}
	close(flight.done)
}
