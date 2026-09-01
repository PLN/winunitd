//go:build windows

package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func helperMode() string {
	return helperModeFrom(os.Args, os.Getenv("WINUNITD_JOB_HELPER"))
}

func TestMain(m *testing.M) {
	switch helperMode() {
	case "sleep":
		select {}
	case "oneshot":
		os.Exit(0)
	case "fail":
		os.Exit(2)
	case "hello":
		fmt.Println("hello-stdout")
		fmt.Fprintln(os.Stderr, "hello-stderr")
		_ = os.Stdout.Sync()
		_ = os.Stderr.Sync()
		select {}
	case "spawn":
		pid, err := startHelperChild(false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "spawn: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("child %d\n", pid)
		_ = os.Stdout.Sync()
		select {}
	case "breakaway":
		if _, err := startHelperChild(true); err != nil {
			fmt.Println("breakaway-denied")
		} else {
			fmt.Println("breakaway-ok")
		}
		pid, err := startHelperChild(false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "child: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("child %d\n", pid)
		_ = os.Stdout.Sync()
		select {}
	case "alloc":
		runAllocUntilKilled()
	case "spawn-hold":
		_, _ = startHelperChild(false)
		select {}
	case "scm-proxy":
		runSCMProxyTestService()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func startHelperChild(breakaway bool) (int, error) {
	cmd := exec.Command(os.Args[0], winunitdHelperArgPrefix+"sleep")
	cmd.Env = helperEnv("WINUNITD_JOB_HELPER=sleep")
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
	if breakaway {
		flags |= windows.CREATE_BREAKAWAY_FROM_JOB
	}
	cmd.SysProcAttr = &windows.SysProcAttr{
		HideWindow:    true,
		CreationFlags: flags,
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

func runAllocUntilKilled() {
	var held [][]byte
	for {
		b := make([]byte, 1<<20)
		for i := 0; i < len(b); i += 4096 {
			b[i] = 1
		}
		held = append(held, b)
	}
}
