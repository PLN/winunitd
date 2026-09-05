//go:build windows

// Command msi-fixture is a disposable-lab service, never the product daemon.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/windows/svc"
)

const serviceName = "winunitd-msi-fixture"

var version = "development"

type service struct{ dir string }

func main() {
	if len(os.Args) == 2 && os.Args[1] == "fail" {
		os.Exit(1) // Deliberate deferred MSI failure, after file/service mutation.
	}
	if len(os.Args) >= 2 {
		token := ""
		if len(os.Args) == 3 {
			token = os.Args[2]
		}
		if err := maintenance(os.Args[1], token); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	exe, err := os.Executable()
	if err == nil {
		err = svc.Run(serviceName, service{dir: filepath.Dir(exe)})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func (s service) record(event string) error {
	f, err := os.OpenFile(filepath.Join(s.dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(struct {
		Time    string `json:"time"`
		Event   string `json:"event"`
		Version string `json:"version"`
		PID     int    `json:"pid"`
	}{time.Now().UTC().Format(time.RFC3339Nano), event, version, os.Getpid()})
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (s service) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending, WaitHint: 10000}
	if err := s.record("start"); err != nil {
		return false, 1
	}
	running := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPreShutdown}
	status <- running
	for req := range requests {
		switch req.Cmd {
		case svc.Interrogate:
			status <- running
		case svc.Stop, svc.Shutdown, svc.PreShutdown:
			if err := s.record(fmt.Sprintf("control-%d", req.Cmd)); err != nil {
				return false, 2
			}
			delay := 0
			if data, err := os.ReadFile(filepath.Join(s.dir, "stop-seconds")); err == nil {
				if _, err := fmt.Sscanf(string(data), "%d", &delay); err != nil || delay < 0 || delay > 240 {
					return false, 3
				}
			} else if !os.IsNotExist(err) {
				return false, 3
			}
			for i := 0; i < delay; i++ {
				status <- svc.Status{State: svc.StopPending, CheckPoint: uint32(i + 1), WaitHint: 10000}
				time.Sleep(time.Second)
			}
			if err := s.record("stopped-after-" + strconv.Itoa(delay)); err != nil {
				return false, 4
			}
			return false, 0
		}
	}
	return false, 5
}
