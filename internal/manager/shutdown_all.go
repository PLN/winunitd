package manager

import (
	"context"
	"errors"
	"fmt"
)

// ShutdownAll seals both admission domains before either begins teardown. System
// and user cleanup share one caller deadline and progress independently. Success
// includes manager-owned controls and journal closure; the caller still owns the
// control listener and broker job. Admission stays closed until process restart.
func ShutdownAll(ctx context.Context, units *Manager, users *UserHost) error {
	if ctx == nil {
		ctx = context.Background()
	}
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
		select {
		case err := <-results:
			result = errors.Join(result, err)
		case <-ctx.Done():
			return errors.Join(result, ctx.Err())
		}
	}
	return errors.Join(result, ctx.Err())
}
