//go:build windows

package headless

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"golang.org/x/sys/windows"
)

// WorkloadPipe is the workload's command pipe for its account: SYSTEM and
// the account itself may connect.
func WorkloadPipe(sid string) string { return QualificationPipePrefix + `workload\` + sid }

// Progress is the workload's flushed liveness record.
type Progress struct {
	Sequence         uint64 `json:"sequence"`
	Nonce            string `json:"nonce"`
	Time             string `json:"time"`
	PID              uint32 `json:"pid"`
	Created          uint64 `json:"created"`
	SID              string `json:"sid"`
	AuthenticationID string `json:"authenticationId"`
	Session          uint32 `json:"session"`
}

// Command is one enumerated request on the workload pipe.
type Command struct {
	Verb  string `json:"verb"`            // health, probe or exit
	Probe string `json:"probe,omitempty"` // a probe name below
	Code  uint32 `json:"code,omitempty"`  // exit code
}

// Reply answers one command.
type Reply struct {
	OK       bool            `json:"ok"`
	Error    string          `json:"error,omitempty"`
	Progress *Progress       `json:"progress,omitempty"`
	Probe    json.RawMessage `json:"probe,omitempty"`
}

// probeArgs maps an enumerated probe name to the fixture's own arguments;
// targets come from the state root's config, never from the request.
func probeArgs(name, config, out, state string) ([]string, bool) {
	nonce := newNonce()
	switch name {
	case "token":
		return []string{"probe-token", "--out", out}, true
	case "paths-own":
		return []string{"probe-path", "--set", PathsOwnRoots, "--config", config, "--state", state, "--nonce", nonce, "--out", out}, true
	case "paths-missing":
		return []string{"probe-path", "--set", PathsMissingAndDenied, "--config", config, "--state", state, "--nonce", nonce, "--out", out}, true
	case "tcp-loopback":
		return []string{"probe-tcp", "--target", "loopback", "--config", config, "--nonce", nonce, "--out", out}, true
	case "tcp-peer":
		return []string{"probe-tcp", "--target", "peer", "--config", config, "--nonce", nonce, "--out", out}, true
	case "smb":
		return []string{"probe-smb", "--config", config, "--nonce", nonce, "--out", out}, true
	case "efs":
		return []string{"probe-efs", "--config", config, "--out", out}, true
	case "pipe":
		return []string{"probe-pipe", "--from-config", config, "--client", ClientInUnit, "--out", out}, true
	}
	return nil, false
}

func newNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// defaultStateRoot is the workload's root under its own token's
// LocalAppData.
func defaultStateRoot() (string, error) {
	tp, err := ProbeOwnToken()
	if err != nil {
		return "", err
	}
	local, ok := tp.KnownFolders["localAppData"]
	if !ok {
		return "", errors.New("the token resolves no LocalAppData")
	}
	return filepath.Join(local, "winunitd-qual", "headless-workload"), nil
}

type workload struct {
	state, config string
	exe           string
	mu            sync.Mutex
	progress      Progress
	probes        int
	exit          chan uint32
}

func (w *workload) tick() error {
	w.mu.Lock()
	w.progress.Sequence++
	w.progress.Nonce = newNonce()
	w.progress.Time = time.Now().UTC().Format(time.RFC3339Nano)
	p := w.progress
	w.mu.Unlock()
	return writeJSONFile(filepath.Join(w.state, "progress.json"), p)
}

// runProbe runs one probe as a child of the workload, inside its unit job,
// bounded in time; a probe that does not finish is killed.
func (w *workload) runProbe(name string) (json.RawMessage, error) {
	w.mu.Lock()
	w.probes++
	n := w.probes
	w.mu.Unlock()
	out := filepath.Join(w.state, "probes", fmt.Sprintf("%s-%04d.json", name, n))
	args, ok := probeArgs(name, w.config, out, w.state)
	if !ok {
		return nil, fmt.Errorf("unknown probe %q", name)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, w.exe, args...)
	cmd.SysProcAttr = &windows.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("probe %s: %w", name, err)
	}
	data, err := readBounded(out)
	return json.RawMessage(data), err
}

func (w *workload) answer(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(4 * probeTimeout))
	line, err := bufio.NewReader(&limitedReader{c, 1024}).ReadString('\n')
	reply := Reply{}
	var cmd Command
	switch {
	case err != nil:
		reply.Error = "no command"
	case decodeStrict([]byte(line), &cmd) != nil:
		reply.Error = "malformed command"
	case cmd.Verb == "health":
		w.mu.Lock()
		p := w.progress
		w.mu.Unlock()
		reply.OK, reply.Progress = true, &p
	case cmd.Verb == "probe":
		reply.Probe, err = w.runProbe(cmd.Probe)
		reply.OK = err == nil
		if err != nil {
			reply.Error = err.Error()
		}
	case cmd.Verb == "exit":
		reply.OK = true
		defer func() { w.exit <- cmd.Code }()
	default:
		reply.Error = "unknown verb"
	}
	data, _ := json.Marshal(reply)
	_, _ = c.Write(append(data, '\n'))
}

func runServe(args []string) error {
	fs := newFlags("serve")
	state := fs.String("state", "", "")
	interval := fs.Duration("interval", 5*time.Second, "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := durationIn("interval", *interval, 100*time.Millisecond, time.Minute); err != nil {
		return err
	}
	if *state == "" {
		root, err := defaultStateRoot()
		if err != nil {
			return err
		}
		*state = root
	} else if err := absPath("state", *state); err != nil {
		return err
	}
	if err := os.MkdirAll(*state, 0o700); err != nil {
		return err
	}
	tp, err := ProbeOwnToken()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	created, err := creationTime(windows.CurrentProcess())
	if err != nil {
		return err
	}
	w := &workload{state: *state, config: filepath.Join(*state, "config.json"), exe: exe, exit: make(chan uint32, 1),
		progress: Progress{PID: windows.GetCurrentProcessId(), Created: created, SID: tp.SID, AuthenticationID: tp.AuthenticationID, Session: tp.Session}}
	if err := w.tick(); err != nil {
		return err
	}
	if cfg, err := LoadProbeConfig(w.config); err == nil && cfg.Loopback != "" {
		echo, err := ListenEcho(cfg.Loopback)
		if err != nil {
			return fmt.Errorf("loopback echo: %w", err)
		}
		defer echo.Close()
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := protocol.ListenPipeSDDL(WorkloadPipe(tp.SID), "D:P(A;;GA;;;SY)(A;;GA;;;"+tp.SID+")")
	if err != nil {
		return fmt.Errorf("command pipe: %w", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go w.answer(c)
		}
	}()
	t := time.NewTicker(*interval)
	defer t.Stop()
	for {
		select {
		case code := <-w.exit:
			return exitCode(code)
		case <-t.C:
			if err := w.tick(); err != nil {
				return err
			}
		}
	}
}

// runFail exits with code until the release file exists, then serves. Each
// failing run first lives for --hold, so that the observer, which scans at
// most MaxObservationGap apart, holds every one.
func runFail(args []string) error {
	fs := newFlags("fail")
	code := fs.Uint("code", 7, "")
	until := fs.String("until", "", "")
	hold := fs.Duration("hold", 0, "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := absPath("until", *until); err != nil {
		return err
	}
	if *code == 0 || *code > 255 {
		return usage("--code must be 1 to 255")
	}
	if err := durationIn("hold", *hold, 0, 10*time.Second); err != nil {
		return err
	}
	if _, err := os.Stat(*until); err == nil {
		return runServe(nil)
	}
	time.Sleep(*hold)
	return exitCode(uint32(*code))
}
