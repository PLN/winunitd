package manager

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PLN/winunitd/internal/protocol"
)

const (
	DefaultMaxStopTransactions = 32
	completedOperationLimit    = 256
	operationErrorLimit        = 4096
)

// Call under m.mu only after plan validation and worker admission. Pending
// records are never evicted; their count follows start/stop admission limits.
func (m *Manager) beginOperationLocked(name, action string, origin activationOrigin, members []string) string {
	m.operationSequence++
	id := fmt.Sprintf("%s/op/%d", m.configNamespace, m.operationSequence)
	source := "explicit"
	switch origin.(type) {
	case *timerOrigin:
		source = "timer"
	case *watchOrigin:
		source = "watch"
	}
	if m.operations == nil {
		m.operations = make(map[string]*protocol.OperationResult)
	}
	m.operations[id] = &protocol.OperationResult{
		ID: id, Unit: strings.Clone(name), Action: action, Origin: source, State: "running",
		ConfigRevision: m.configRevision, StartedAt: m.now().UTC().Format(time.RFC3339Nano),
	}
	for _, member := range members {
		if rt := m.units[member]; rt != nil {
			rt.lastOperationID = id
		}
	}
	return id
}

func (m *Manager) finishOperation(id string, result *protocol.UnitResult, err error) (*protocol.UnitResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op := m.operations[id]
	op.State = "succeeded"
	op.CompletedAt = m.now().UTC().Format(time.RFC3339Nano)
	if err != nil {
		op.State = "failed"
		op.Error = strings.ToValidUTF8(err.Error(), "\uFFFD")
		if len(op.Error) > operationErrorLimit {
			op.Error = op.Error[:operationErrorLimit]
			for !utf8.ValidString(op.Error) {
				op.Error = op.Error[:len(op.Error)-1]
			}
			op.ErrorTruncated = true
		}
		// Slicing a long error must not retain its entire backing allocation.
		op.Error = strings.Clone(op.Error)
		pe := protocol.ErrFailed(err.Error())
		var original *protocol.Error
		if errors.As(err, &original) {
			copy := *original
			pe = &copy
		}
		pe.OperationID = id
		err = pe
	}
	if result != nil {
		result.OperationID = id
	}
	m.completedOperations = append(m.completedOperations, id)
	if len(m.completedOperations) > completedOperationLimit {
		delete(m.operations, m.completedOperations[0])
		copy(m.completedOperations, m.completedOperations[1:])
		m.completedOperations = m.completedOperations[:completedOperationLimit]
	}
	return result, err
}

// Operation returns a copy; callers cannot mutate retained history.
func (m *Manager) Operation(id string) (*protocol.OperationResult, error) {
	if id == "" || len(id) > 128 || strings.TrimSpace(id) != id {
		return nil, protocol.ErrInvalidParams("operation ID required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	op := m.operations[id]
	if op == nil {
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "operation is unknown or no longer retained"}
	}
	result := *op
	return &result, nil
}
