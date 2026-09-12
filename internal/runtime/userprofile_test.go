package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type profileTestResource struct {
	steps *[]string
	fail  bool
}

func (p *profileTestResource) Close() error {
	*p.steps = append(*p.steps, "unload")
	if p.fail {
		return errors.New("profile busy")
	}
	return nil
}

type profileTestProcess struct {
	steps       *[]string
	fail, alive bool
}

func (p *profileTestProcess) PID() int                   { return 1 }
func (p *profileTestProcess) SID() string                { return "test" }
func (p *profileTestProcess) Alive() bool                { return p.alive }
func (p *profileTestProcess) Wait(context.Context) error { return nil }
func (p *profileTestProcess) Kill() error {
	*p.steps = append(*p.steps, "kill")
	if p.fail {
		return errors.New("termination unconfirmed")
	}
	p.alive = false
	return nil
}

func TestUserProfileRetainedThroughCleanupFailures(t *testing.T) {
	var steps []string
	proc := &profileTestProcess{steps: &steps, fail: true, alive: true}
	profile := &profileTestResource{steps: &steps, fail: true}
	owner, err := finishProfileLaunch("test", proc, profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Kill() == nil || !reflect.DeepEqual(steps, []string{"kill"}) {
		t.Fatal("profile released before confirmed process cleanup")
	}
	proc.fail = false
	if owner.Kill() == nil || !reflect.DeepEqual(steps, []string{"kill", "kill", "unload"}) {
		t.Fatal("failed unload not reported")
	}
	profile.fail = false
	if err := owner.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Kill(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(steps, []string{"kill", "kill", "unload", "unload"}) {
		t.Fatal("cleanup retry repeated completed effects")
	}
}

func TestUserProfileFailedCreationRetainsUnload(t *testing.T) {
	var steps []string
	profile := &profileTestResource{steps: &steps, fail: true}
	cause := errors.New("creation failed")
	owner, err := finishProfileLaunch("test", nil, profile, cause)
	if owner == nil || !errors.Is(err, cause) || owner.Alive() || owner.PID() != 0 {
		t.Fatal("failed pre-process cleanup lost ownership")
	}
	profile.fail = false
	if err := owner.Kill(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(steps, []string{"unload", "unload"}) {
		t.Fatal("profile cleanup not retried")
	}
}
