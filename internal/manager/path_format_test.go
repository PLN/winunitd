package manager

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestPathFormatPreservesLegacyANDAndSelectsV2OR(t *testing.T) {
	for _, tc := range []struct {
		name, header, directive string
		any                     bool
	}{
		{"legacy", "", "PathExists", false},
		{"v2-any", "[Unit]\nFormatVersion=2\n", "PathExists", true},
		{"v2-all", "[Unit]\nFormatVersion=2\n", "PathExistsAll", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const first = `C:\Data\one`
			const second = `C:\Data\two`
			launch := &fakeLauncher{}
			hub := newFakePathHub()
			hub.SetExists(first, true)
			hub.acknowledgements = map[string]chan struct{}{first: make(chan struct{}, 8), second: make(chan struct{}, 8)}
			m := managerWithExists(t, launch, hub, map[string]string{
				"ready.service": "[Service]\nExecStart=C:\\Apps\\worker.exe\nWorkingDirectory=C:\\Apps\n",
				"ready.path":    tc.header + fmt.Sprintf("[Path]\n%s=%s\n%s=%s\n", tc.directive, first, tc.directive, second),
			})
			if _, err := m.Start(context.Background(), "ready.path"); err != nil {
				t.Fatal(err)
			}
			waitWatchCycle(t, hub.acknowledgements[first])
			waitWatchCycle(t, hub.acknowledgements[second])
			if tc.any {
				waitUntil(t, 2*time.Second, func() bool { return len(launch.units()) == 1 })
			} else if len(launch.units()) != 0 {
				t.Fatal("AND predicate started with only one existing path")
			}
			hub.SetExists(second, true)
			waitWatchCycle(t, hub.acknowledgements[second])
			waitUntil(t, 2*time.Second, func() bool { return len(launch.units()) == 1 })
			hub.SetExists(first, false)
			waitWatchCycle(t, hub.acknowledgements[first])
			if len(launch.units()) != 1 {
				t.Fatal("path observation restarted a running companion")
			}
		})
	}
}
