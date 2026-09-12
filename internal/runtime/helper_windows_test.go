//go:build windows

package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
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
	case "stdio-write":
		runStdioWriteHelper()
		os.Exit(0)
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

func runStdioWriteHelper() {
	var startup windows.StartupInfo
	startupOK := windows.GetStartupInfo(&startup) == nil && startup.Flags&windows.STARTF_USESTDHANDLES == 0
	stdoutOK := writeStdHandle(windows.STD_OUTPUT_HANDLE, []byte("um-stdout\n")) == nil
	stderrOK := writeStdHandle(windows.STD_ERROR_HANDLE, []byte("um-stderr\n")) == nil
	_, goOutErr := os.Stdout.WriteString("go-stdout\n")
	_, goErrErr := os.Stderr.WriteString("go-stderr\n")
	pipeName := os.Getenv("WINUNITD_STDIO_REPORT_PIPE")
	for _, arg := range os.Args {
		if value, ok := strings.CutPrefix(arg, "--stdio-report-pipe="); ok {
			pipeName = value
		}
	}
	if pipeName == "" {
		if stdoutOK && stderrOK {
			os.Exit(0)
		}
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := winio.DialPipeContext(ctx, pipeName)
	if err != nil {
		os.Exit(2)
	}
	defer c.Close()
	stdout := "err"
	if stdoutOK {
		stdout = "ok"
	}
	stderr := "err"
	if stderrOK {
		stderr = "ok"
	}
	_, brokerVar := os.LookupEnv("WINUNITD_BROKER_ONLY")
	info, infoErr := CurrentUserInfo()
	foldersOK := infoErr == nil && info.LocalAppData != "" && info.RoamingAppData != "" &&
		os.Getenv("LOCALAPPDATA") == info.LocalAppData && os.Getenv("APPDATA") == info.RoamingAppData
	_, _ = fmt.Fprintf(c, "stdout=%s stderr=%s go-stdio=%t default-stdio=%t broker-env=%t folders=%t\n",
		stdout, stderr, goOutErr == nil && goErrErr == nil, startupOK, brokerVar, foldersOK)
}

func writeStdHandle(std uint32, data []byte) error {
	h, err := windows.GetStdHandle(std)
	if err != nil {
		return err
	}
	if h == 0 || h == windows.InvalidHandle {
		return fmt.Errorf("invalid std handle")
	}
	var written uint32
	return windows.WriteFile(h, data, &written, nil)
}
