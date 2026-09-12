package manager

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/PLN/winunitd/internal/timers"
)

// ShutdownAll seals both admission domains before either begins teardown. System
// and user cleanup share one caller deadline and progress independently. Success
// includes manager-owned controls and journal closure; the caller still owns the
// control listener and broker job. Admission stays closed until process restart.
func ShutdownAll(ctx context.Context, units *Manager, users *UserHost) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var stops *stopSet
	if units != nil {
		stops = &units.stops
	} else if users != nil {
		stops = &users.stops
	} else {
		return ctx.Err()
	}
	for {
		var ownsPass atomic.Bool
		err := stops.wait(ctx, timers.DefaultClock(), stopKey{allSystem: units, allUsers: users}, 0, func() error {
			ownsPass.Store(true)
			return shutdownAllPass(ctx, units, users)
		})
		if ctx.Err() == nil && !ownsPass.Load() && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
			continue
		}
		return errors.Join(err, ctx.Err())
	}
}

// The outer retained pass bounds caller waits. Join both workers here even
// after deadline, so a slow decision lock cannot multiply workers on retries.
func shutdownAllPass(ctx context.Context, units *Manager, users *UserHost) error {
	// This is the only combined lock order: manager, then user host. Holding
	// both publishes one barrier before workers can observe either domain.
	if units != nil {
		units.mu.Lock()
	}
	if users != nil {
		users.mu.Lock()
	}
	if units != nil {
		units.sealShutdownLocked()
	}
	if users != nil {
		users.sealShutdownLocked()
		users.mu.Unlock()
	}
	if units != nil {
		units.mu.Unlock()
	}
	results := make(chan error, 2)
	go func() {
		err := units.Shutdown(ctx)
		err = errors.Join(err, units.CloseContext(ctx))
		if err != nil {
			err = fmt.Errorf("system manager shutdown: %w", err)
		}
		results <- err
	}()
	go func() {
		err := users.Shutdown(ctx)
		if err != nil {
			err = fmt.Errorf("user host shutdown: %w", err)
		}
		results <- err
	}()
	var result error
	for i := 0; i < 2; i++ {
		result = errors.Join(result, <-results)
	}
	return errors.Join(result, ctx.Err())
}
