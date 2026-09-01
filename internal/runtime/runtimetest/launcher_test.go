package runtimetest

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

func TestLauncherStartStop(t *testing.T) {
	t.Parallel()
	p, err := Launcher().Start(context.Background(), runtime.StartSpec{
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

func TestWindowsProductionOmitsStubLauncher(t *testing.T) {
	env := append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	cmd := exec.Command("go", "list", "-f", "{{range .GoFiles}}{{.}}\n{{end}}", "github.com/PLN/winunitd/internal/runtime")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list runtime: %v\n%s", err, out)
	}
	files := string(out)
	if strings.Contains(files, "exec_stub.go") {
		t.Fatalf("exec_stub.go is in the Windows production package:\n%s", files)
	}

	cmd = exec.Command("go", "list", "-f", "{{join .Deps \"\\n\"}}", "github.com/PLN/winunitd/cmd/winunitd")
	cmd.Env = env
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list winunitd: %v\n%s", err, out)
	}
	deps := string(out)
	if strings.Contains(deps, "internal/runtime/runtimetest") {
		t.Fatalf("runtimetest is imported by Windows winunitd:\n%s", deps)
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "winunitd.exe")
	build := exec.Command("go", "build", "-o", bin, "github.com/PLN/winunitd/cmd/winunitd")
	build.Env = env
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build winunitd: %v\n%s", err, out)
	}
	nm := exec.Command("go", "tool", "nm", bin)
	nmOut, err := nm.CombinedOutput()
	if err != nil {
		t.Fatalf("go tool nm: %v\n%s", err, nmOut)
	}
	if bytes.Contains(nmOut, []byte("StubLauncher")) {
		t.Fatal("StubLauncher symbol present in Windows production binary")
	}
	if bytes.Contains(nmOut, []byte("runtimetest")) {
		t.Fatal("runtimetest symbol present in Windows production binary")
	}
}
