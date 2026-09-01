//go:build !windows

package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/unit"
)

func TestStubStartStop(t *testing.T) {
	p, err := DefaultLauncher().Start(context.Background(), StartSpec{
		Unit: "foo.service",
		Type: unit.TypeSimple,
		Argv: []string{`C:\Tools\foo.exe`},
		Dir:  `C:\Tools`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Alive() {
		t.Fatal("stub process should stay alive until Stop")
	}
	if p.Job() == nil {
		t.Fatal("stub should still expose a Job")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.Wait(ctx); err == nil {
		t.Fatal("Wait returned before Stop")
	}
	if err := p.Stop(time.Second); err != nil {
		t.Fatal(err)
	}
	if p.Alive() {
		t.Fatal("stub still alive after Stop")
	}
}

func TestStubOneshotAndEmptyExec(t *testing.T) {
	p, err := DefaultLauncher().Start(context.Background(), StartSpec{
		Type:         unit.TypeOneshot,
		Argv:         []string{`C:\Tools\init.exe`},
		TimeoutStart: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Close()
	_, err = DefaultLauncher().Start(context.Background(), StartSpec{})
	if err == nil {
		t.Fatal("empty ExecStart should fail")
	}
}

func TestStubUnitJob(t *testing.T) {
	j, err := OpenUnitJob()
	if err != nil {
		t.Fatal(err)
	}
	in, err := j.Contains(1)
	if err != nil {
		t.Fatal(err)
	}
	if in {
		t.Fatal("stub Contains should be false")
	}
	if err := j.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.QueryLimits(); err != nil {
		t.Fatal(err)
	}
	if j.ResourceLimitC() != nil {
		t.Fatal("stub ResourceLimitC must be nil")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
}
