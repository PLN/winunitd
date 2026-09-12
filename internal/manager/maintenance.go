package manager

import (
	"context"
	"sync"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

type maintenanceAttempt struct {
	done              chan struct{}
	started, deadline time.Time
	err               error // published before done closes
}

type maintenanceState struct {
	sync.Mutex
	attempt *maintenanceAttempt
}

// One accepted attempt owns its deadline independently of the calling connection.
// Failed attempts can be retried; underlying shutdown joins retained native work.
// Successful maintenance remains in force until manager process restart.
func (c *Control) enterMaintenance(ctx context.Context, p protocol.MaintenanceParams) (*protocol.MaintenanceResult, error) {
	if c.Units.cfg.UserScope {
		return nil, protocol.ErrMethodNotFound(protocol.MethodMaintenance)
	}
	if p.TimeoutMS < 0 || p.TimeoutMS > protocol.MaxMaintenanceTimeoutMS {
		return nil, protocol.ErrInvalidParams("maintenance timeout must be between 1 and 180000 milliseconds; zero selects the default")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.TimeoutMS == 0 {
		p.TimeoutMS = protocol.MaxMaintenanceTimeoutMS
	}
	c.maintenance.Lock()
	a := c.maintenance.attempt
	if a != nil {
		select {
		case <-a.done:
			if a.err != nil {
				a = nil
			}
		default:
		}
	}
	if a == nil {
		started := time.Now().UTC()
		runCtx, cancel := context.WithTimeout(context.Background(), time.Duration(p.TimeoutMS)*time.Millisecond)
		deadline, _ := runCtx.Deadline()
		a = &maintenanceAttempt{done: make(chan struct{}), started: started, deadline: deadline.UTC()}
		c.maintenance.attempt = a
		go func(attempt *maintenanceAttempt) {
			err := ShutdownAll(runCtx, c.Units, c.Users)
			cancel()
			c.maintenance.Lock()
			attempt.err = err
			close(attempt.done)
			c.maintenance.Unlock()
		}(a)
	}
	c.maintenance.Unlock()
	select {
	case <-a.done:
		if a.err != nil {
			return nil, protocol.ErrFailed("maintenance incomplete: " + a.err.Error())
		}
		return maintenanceResult(a), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func maintenanceResult(a *maintenanceAttempt) *protocol.MaintenanceResult {
	if a == nil {
		return nil
	}
	r := &protocol.MaintenanceResult{State: "quiescing", StartedAt: a.started.Format(time.RFC3339Nano), Deadline: a.deadline.Format(time.RFC3339Nano)}
	select {
	case <-a.done:
		if a.err != nil {
			r.State = "failed"
			r.Error = a.err.Error()
		} else {
			r.State = "quiesced"
		}
	default:
	}
	return r
}

func (c *Control) maintenanceSnapshot() *protocol.MaintenanceResult {
	c.maintenance.Lock()
	defer c.maintenance.Unlock()
	return maintenanceResult(c.maintenance.attempt)
}
