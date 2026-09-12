package manager

import (
	"context"
	"fmt"
	"strings"

	"github.com/PLN/winunitd/internal/protocol"
)

// CancelOperation cancels unfinished start/restart work. Completed members keep
// running; accepted stop operations retain their independent cleanup lifetime.
func (m *Manager) CancelOperation(ctx context.Context, id string) (*protocol.OperationResult, error) {
	if id == "" || len(id) > 128 || strings.TrimSpace(id) != id {
		return nil, protocol.ErrInvalidParams("operation ID required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	op := m.operations[id]
	if op == nil {
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "operation is unknown or no longer retained"}
	}
	if op.State == "running" {
		if op.Action == "stop" {
			return nil, protocol.ErrFailed("accepted stop cannot be canceled; cleanup continues")
		}
		task := m.activeOperations[id]
		if task == nil {
			return nil, &protocol.Error{Code: protocol.CodeBusy, Message: "operation is completing; query or retry"}
		}
		m.cancelOperationLocked(task, fmt.Errorf("canceled by request: %w", context.Canceled))
	}
	result := *op
	return &result, nil
}

// Caller holds m.mu; completion publication uses the same serialization.
func (m *Manager) cancelOperationLocked(task *operationTask, reason error) {
	if task.ctx.Err() != nil {
		return
	}
	// Successful members keep running, as with ordinary partial start failure.
	// In-flight members disarm recovery before a late completion can publish.
	for name, plan := range task.plans {
		rt := m.units[name]
		if plan.completed || !plan.launched || plan.launchGen == 0 || rt != plan.record || rt.gen != plan.launchGen || rt.stopEpoch != plan.stopEpoch {
			continue
		}
		rt.stopping = true
		rt.stopEpoch++
		rt.cancelRestart()
		if rt.startCancel != nil {
			rt.startCancel()
		}
		if rt.proc != nil || rt.invocationUnit != nil {
			rt.setCleanup(cleanupWorkload, true)
		}
	}
	m.operations[task.flight.id].CancellationReason = reason.Error()
	task.cancel(reason)
}
