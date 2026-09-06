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
	id        string
	record    *unitRuntime
	stopEpoch uint64
	revision  string
	done      chan struct{}
	result    *protocol.UnitResult
	err       error
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
	select {
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
