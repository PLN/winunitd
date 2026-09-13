package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

type goEnvironment struct {
	GOROOT    string
	GOVERSION string
}

// Resolve the pinned toolchain through Go's supported selection mechanism, then
// verify the absolute child compiler with automatic toolchain switching disabled.
func pinnedGo(want string, query func(string, []string) (goEnvironment, error)) (string, error) {
	env := buildEnv(os.Environ(), runtime.GOOS, runtime.GOARCH)
	for i, value := range env {
		if value == "GOTOOLCHAIN=local" {
			env[i] = "GOTOOLCHAIN=" + want
		}
	}
	selected, err := query("go", env)
	if err != nil {
		return "", fmt.Errorf("resolve pinned compiler: %w", err)
	}
	if selected.GOVERSION != want || !filepath.IsAbs(selected.GOROOT) {
		return "", fmt.Errorf("selected compiler must be %s with an absolute GOROOT", want)
	}
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(selected.GOROOT, "bin", name)
	verified, err := query(path, buildEnv(os.Environ(), runtime.GOOS, runtime.GOARCH))
	if err != nil {
		return "", fmt.Errorf("verify pinned compiler: %w", err)
	}
	if verified.GOVERSION != want {
		return "", fmt.Errorf("child compiler requires %s, found %s", want, verified.GOVERSION)
	}
	return path, nil
}

func queryGoEnvironment(name string, env []string) (goEnvironment, error) {
	cmd := exec.Command(name, "env", "-json", "GOROOT", "GOVERSION")
	cmd.Env = env
	data, err := cmd.Output()
	if err != nil {
		return goEnvironment{}, err
	}
	var result goEnvironment
	err = json.Unmarshal(data, &result)
	return result, err
}
