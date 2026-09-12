package manager

import (
	"errors"
	"fmt"
	"maps"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

// The caller owns the reserved linger-scan work slot. Serialize disk observation
// and publication with mutations, but never perform I/O under the decision lock.
// Failed/invalid observations cannot retain authority for an unvalidated record.
func (h *UserHost) refreshLingerRecords() ([]runtime.LingerRecord, error) {
	h.lingerIO.Lock()
	read := h.cfg.ListLinger
	if read == nil {
		read = h.store.List
	}
	records, scanErr := read()
	accepted := make(map[string]runtime.LingerRecord)
	if len(records) > maxTrackedUserManagers {
		scanErr = fmt.Errorf("linger record limit %d exceeded", maxTrackedUserManagers)
		records = nil
	}
	valid := make([]runtime.LingerRecord, 0, len(records))
	for _, rec := range records {
		if !protocol.ValidSID(rec.SID) {
			scanErr = errors.Join(scanErr, errors.New("invalid linger record SID"))
			continue
		}
		if _, duplicate := accepted[rec.SID]; duplicate {
			scanErr = errors.Join(scanErr, errors.New("duplicate linger record SID"))
			accepted = map[string]runtime.LingerRecord{}
			valid = nil
			break
		}
		accepted[rec.SID] = rec
		valid = append(valid, rec)
	}
	h.mu.Lock()
	var removed []string
	if !h.closed {
		for sid := range h.lingerRecords {
			if _, present := accepted[sid]; !present {
				removed = append(removed, sid)
			}
		}
		if !maps.Equal(h.lingerRecords, accepted) {
			h.lingerRevision++
		}
		h.lingerRecords = accepted
		h.lingerKnown = true
		h.lingerError = ""
		if scanErr != nil {
			h.lingerError = scanErr.Error()
		}
	}
	h.mu.Unlock()
	h.lingerIO.Unlock()
	for _, sid := range removed {
		h.queueIdleCleanup(sid)
	}
	return valid, scanErr
}
