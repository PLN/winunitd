package main

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPinnedGoVerifiesAbsoluteChildCompiler(t *testing.T) {
	root := t.TempDir()
	const want = "go1.27.1"
	for _, scenario := range []string{"pinned", "wrong-selected", "relative-root", "wrong-child", "query-error"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			got, err := pinnedGo(want, func(name string, env []string) (goEnvironment, error) {
				calls++
				result := goEnvironment{GOROOT: root, GOVERSION: want}
				if calls == 1 {
					if name != "go" || !slices.Contains(env, "GOTOOLCHAIN="+want) {
						t.Fatal("pin was not selected through Go")
					}
					switch scenario {
					case "wrong-selected":
						result.GOVERSION = "go1.26.0"
					case "relative-root":
						result.GOROOT = "relative"
					case "query-error":
						return goEnvironment{}, errors.New("compiler unavailable")
					}
				} else {
					if filepath.Dir(name) != filepath.Join(root, "bin") || !slices.Contains(env, "GOTOOLCHAIN=local") {
						t.Fatal("absolute child compiler was not checked with switching disabled")
					}
					if scenario == "wrong-child" {
						result.GOVERSION = "go1.26.0"
					}
				}
				return result, nil
			})
			if scenario == "pinned" {
				if err != nil || !filepath.IsAbs(got) || calls != 2 {
					t.Fatalf("pinned compiler: %q, %v, %d queries", got, err, calls)
				}
			} else if err == nil || got != "" {
				t.Fatalf("invalid compiler accepted: %q %v", got, err)
			}
		})
	}
}

func TestBuildEnvRemovesInheritedCompilerOverrides(t *testing.T) {
	env := buildEnv([]string{"GOROOT=stale", "GoRoot=also-stale", "GOTOOLCHAIN=auto", "GOFLAGS=-race", "PATH=keep"}, "windows", "amd64")
	for _, entry := range env {
		if strings.HasPrefix(strings.ToUpper(entry), "GOROOT=") {
			t.Fatal("inherited compiler root survived")
		}
	}
	if !slices.Contains(env, "GOTOOLCHAIN=local") || !slices.Contains(env, "GOFLAGS=") || !slices.Contains(env, "PATH=keep") {
		t.Fatalf("incorrect build environment: %v", env)
	}
}
