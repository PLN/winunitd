package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
)

// A profile is an owned launch resource even when process creation fails.
// Keep it available for retry until both the process tree and profile are gone.
type profiledUserManager struct {
	mu      sync.Mutex
	proc    UserManagerProc
	profile io.Closer
	sid     string
	stopped bool
}

func finishProfileLaunch(sid string, proc UserManagerProc, profile io.Closer, cause error) (UserManagerProc, error) {
	if profile == nil {
		return proc, cause
	}
	owner := &profiledUserManager{proc: proc, profile: profile, sid: sid}
	if cause != nil {
		if err := owner.Kill(); err != nil {
			return owner, errors.Join(cause, err)
		}
		return nil, cause
	}
	return owner, nil
}

func (p *profiledUserManager) PID() int {
	if p.proc == nil {
		return 0
	}
	return p.proc.PID()
}
func (p *profiledUserManager) SID() string { return p.sid }
func (p *profiledUserManager) Alive() bool { return p.proc != nil && p.proc.Alive() }
func (p *profiledUserManager) Wait(ctx context.Context) error {
	if p.proc == nil {
		return nil
	}
	return p.proc.Wait(ctx)
}
func (p *profiledUserManager) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.stopped && p.proc != nil {
		if err := p.proc.Kill(); err != nil {
			return err
		}
		if p.proc.Alive() {
			return errors.New("user manager remains alive before profile unload")
		}
	}
	p.stopped = true
	if p.profile != nil {
		if err := p.profile.Close(); err != nil {
			return err
		}
		p.profile = nil
	}
	return nil
}
