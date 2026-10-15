package manager

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

// processOwner is a unit job copied for a membership query after the manager
// mutex is released.
type processOwner struct {
	unit       string
	invocation string
	mainPID    int
	stopHelper bool
	job        runtime.Job
}

// UnitOfProcess reports which unit's job holds pid: the workload job, which
// includes descendants, or the ExecStop helper's job. One process handle is
// held for the whole query. Membership comes only from the jobs; a main PID
// match alone is not enough.
func (m *Manager) UnitOfProcess(pid int) (*protocol.UnitOfProcessResult, error) {
	if pid <= 0 || int64(pid) > math.MaxUint32 {
		return nil, protocol.ErrInvalidParams(fmt.Sprintf("invalid pid %d", pid))
	}
	probe, err := runtime.OpenProcessProbe(pid)
	if err != nil {
		switch {
		case errors.Is(err, runtime.ErrNoProcess):
			return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("process %d does not exist", pid)}
		case errors.Is(err, runtime.ErrProcessAccess):
			return nil, &protocol.Error{Code: protocol.CodePermissionDenied, Message: fmt.Sprintf("cannot query process %d", pid)}
		}
		return nil, protocol.ErrFailed(err.Error())
	}
	defer probe.Close()
	for _, owner := range m.processOwners() {
		member, ok := owner.job.(runtime.ProcessMember)
		if !ok {
			continue
		}
		in, err := member.HasProcess(probe)
		if errors.Is(err, runtime.ErrJobClosed) {
			continue // released after the copy; it no longer owns anything
		}
		if err != nil {
			return nil, protocol.ErrFailed(fmt.Sprintf("job membership of process %d in %s: %v", pid, owner.unit, err))
		}
		if !in {
			continue
		}
		res := &protocol.UnitOfProcessResult{
			PID:              pid,
			Scope:            "system",
			Unit:             owner.unit,
			InvocationID:     owner.invocation,
			Main:             !owner.stopHelper && owner.mainPID == pid,
			StopHelper:       owner.stopHelper,
			ProcessCreatedAt: probe.Created().Format(time.RFC3339Nano),
		}
		if m.cfg.UserScope {
			res.Scope = "user"
			res.SID = m.cfg.NotifySID
		}
		return res, nil
	}
	return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("process %d belongs to no unit of this manager", pid)}
}

// processOwners copies every retained workload and stop-helper job, whatever
// the unit's state: a stopping unit still owns its processes until cleanup
// releases the job.
func (m *Manager) processOwners() []processOwner {
	m.mu.Lock()
	defer m.mu.Unlock()
	owners := make([]processOwner, 0, len(m.units)+len(m.stopHelpers))
	for name, rt := range m.units {
		if rt.proc == nil {
			continue
		}
		if job := rt.proc.Job(); job != nil {
			owners = append(owners, processOwner{unit: name, invocation: rt.invocation, mainPID: rt.mainPID, job: job})
		}
	}
	for h := range m.stopHelpers {
		if h.process == nil {
			continue
		}
		if job := h.process.Job(); job != nil {
			owners = append(owners, processOwner{unit: h.owner.name, invocation: h.invocation, stopHelper: true, job: job})
		}
	}
	return owners
}
