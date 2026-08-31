//go:build windows

package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestMain(m *testing.M) {
	switch os.Getenv("WINUNITD_JOB_HELPER") {
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
	}
	os.Exit(m.Run())
}

func startHelperChild(breakaway bool) (int, error) {
	cmd := exec.Command(os.Args[0])
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
