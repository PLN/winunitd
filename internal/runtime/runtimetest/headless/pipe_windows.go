//go:build windows

package headless

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"golang.org/x/sys/windows"
)

// settleWindow is how long the server watches a held caller after reading
// its claim: a caller that exits within it is rejected as exited.
const settleWindow = time.Second

var procImpersonateNamedPipeClient = advapi32.NewProc("ImpersonateNamedPipeClient")

func pipeHandle(c net.Conn) (windows.Handle, bool) {
	if f, ok := c.(interface{ Fd() uintptr }); ok {
		return windows.Handle(f.Fd()), true
	}
	sc, ok := c.(syscall.Conn)
	if !ok {
		return 0, false
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var h windows.Handle
	if err := raw.Control(func(fd uintptr) { h = windows.Handle(fd) }); err != nil {
		return 0, false
	}
	return h, true
}

// impersonationSID impersonates the pipe client at the Identification level
// it dialed with, on a dedicated locked thread, and reverts. A thread whose
// revert fails is retired with its goroutine.
func impersonationSID(h windows.Handle) (string, uint32) {
	type result struct {
		sid  string
		code uint32
	}
	done := make(chan result, 1)
	go func() {
		goruntime.LockOSThread()
		unlock := true
		defer func() {
			if unlock {
				goruntime.UnlockOSThread()
			}
		}()
		if r, _, e := procImpersonateNamedPipeClient.Call(uintptr(h)); r == 0 {
			done <- result{code: win32Code(e)}
			return
		}
		var res result
		var tok windows.Token
		if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &tok); err != nil {
			res.code = win32Code(err)
		} else {
			if user, err := tok.GetTokenUser(); err != nil {
				res.code = win32Code(err)
			} else {
				res.sid = user.User.Sid.String()
			}
			_ = tok.Close()
		}
		if err := windows.RevertToSelf(); err != nil {
			unlock = false
			res = result{code: win32Code(err)}
		}
		done <- res
	}()
	r := <-done
	return r.sid, r.code
}

func processSID(proc windows.Handle) (string, error) {
	var tok windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &tok); err != nil {
		return "", err
	}
	defer tok.Close()
	user, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

func creationTime(proc windows.Handle) (uint64, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(proc, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), nil
}

// Request errors of the qualification pipe.
const (
	requestMissing   = "missing"
	requestMalformed = "malformed"
)

// serveCaller observes, decides and answers one connection, holding the
// caller's process until the connection ends. In order: it holds the
// caller's incarnation by the PID Windows reports, sends ready, reads one
// bounded request, and only then impersonates the caller, because a pipe
// server can impersonate only the context of a message it has read. A
// missing, oversized or malformed request is refused, never read as a
// request without a claim.
func serveCaller(c net.Conn, allowed string) ServerEntry {
	defer c.Close()
	var o CallerObservation
	h, ok := pipeHandle(c)
	if !ok {
		o.OpenError = uint32(windows.ERROR_INVALID_HANDLE)
		return ServerEntry{Observation: o, Verdict: Verdict{Reason: ReasonOpen}}
	}
	if err := windows.GetNamedPipeClientProcessId(h, &o.PID); err != nil {
		o.OpenError = win32Code(err)
	}
	var proc windows.Handle
	if o.OpenError == 0 {
		var err error
		proc, err = windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, o.PID)
		if err != nil {
			o.OpenError = win32Code(err)
		}
	}
	if proc != 0 {
		defer windows.CloseHandle(proc)
		var err error
		if o.Created, err = creationTime(proc); err == nil {
			o.ProcessSID, err = processSID(proc)
		}
		if err != nil {
			o.OpenError = win32Code(err)
		}
	}
	_ = c.SetDeadline(time.Now().Add(probeTimeout))
	w := bufio.NewWriter(c)
	_, _ = w.WriteString("ready\n")
	_ = w.Flush()
	o.Claim, o.RequestError = readRequest(c)
	if o.RequestError == "" {
		o.ImpersonationSID, o.ImpersonationError = impersonationSID(h)
	}
	if proc != 0 {
		state, err := windows.WaitForSingleObject(proc, uint32(settleWindow.Milliseconds()))
		o.Exited = err != nil || state != uint32(windows.WAIT_TIMEOUT)
	}
	accepted, reason := Decide(allowed, o)
	v := Verdict{Accepted: accepted, Reason: reason, Held: proc != 0}
	if accepted {
		v.PID, v.Created = o.PID, o.Created
	}
	data, _ := json.Marshal(v)
	_, _ = c.Write(append(data, '\n'))
	// Hold the caller until it hangs up, bounded by the deadline.
	_, _ = bufio.NewReader(c).ReadString('\n')
	return ServerEntry{Observation: o, Verdict: v}
}

// readRequest reads the caller's one request line: "{}" without a claim,
// or a claim.
func readRequest(c net.Conn) (*Claim, string) {
	line, err := bufio.NewReader(&limitedReader{c, 4096}).ReadString('\n')
	if err != nil {
		return nil, requestMissing
	}
	if strings.TrimSpace(line) == "{}" {
		return nil, ""
	}
	var claim Claim
	if decodeStrict([]byte(line), &claim) != nil || claim.PID == 0 || claim.Created == 0 {
		return nil, requestMalformed
	}
	return &claim, ""
}

// ListenQualificationPipe creates the pipe owned by this (SYSTEM) process
// with a protected DACL granting SYSTEM and the listed accounts.
func ListenQualificationPipe(name string, acl []string) (net.Listener, error) {
	sddl := "D:P(A;;GA;;;SY)"
	for _, sid := range acl {
		if sid != SystemSID {
			sddl += "(A;;GA;;;" + sid + ")"
		}
	}
	return protocol.ListenPipeSDDL(name, sddl)
}

func runPipeServe(args []string) error {
	fs := newFlags("pipe-serve")
	name := fs.String("name", "", "")
	allow := fs.String("allow", "", "")
	var acl listFlag
	fs.Var(&acl, "acl", "")
	report := fs.String("report", "", "")
	maxConns := fs.Int("max", 16, "")
	duration := fs.Duration("duration", 10*time.Minute, "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if !strings.HasPrefix(*name, QualificationPipePrefix) || len(*name) > 200 {
		return usage("--name must be under %s", QualificationPipePrefix)
	}
	if len(acl) == 0 {
		acl = listFlag{*allow}
	}
	for _, sid := range append([]string{*allow}, acl...) {
		if !protocol.ValidSID(sid) {
			return usage("--allow and --acl need valid SIDs")
		}
	}
	if err := errors.Join(absPath("report", *report), durationIn("duration", *duration, time.Second, time.Hour)); err != nil {
		return err
	}
	if *maxConns < 1 || *maxConns > 64 {
		return usage("--max must be 1 to 64")
	}
	// The qualification server is SYSTEM's: its own identity is part of
	// the report, and anything else refuses to serve.
	server, err := selfClaim()
	if err != nil {
		return err
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(server.PID, &session); err != nil {
		return err
	}
	if server.SID != SystemSID || session != 0 {
		return errors.New("the qualification pipe server must run as SYSTEM in session zero")
	}
	ln, err := ListenQualificationPipe(*name, acl)
	if err != nil {
		return err
	}
	rep := ServerReport{Name: *name, Allowed: *allow, ACL: acl, Server: ServerIdentity{PID: server.PID, Created: server.Created, SID: server.SID, Session: session}}
	var mu sync.Mutex
	write := func() error {
		mu.Lock()
		defer mu.Unlock()
		return writeJSONFile(*report, rep)
	}
	if err := write(); err != nil {
		_ = ln.Close()
		return err
	}
	timer := time.AfterFunc(*duration, func() { _ = ln.Close() })
	defer timer.Stop()
	var errs []error
	for n := 0; n < *maxConns; n++ {
		c, err := ln.Accept()
		if err != nil {
			break
		}
		entry := serveCaller(c, *allow)
		mu.Lock()
		rep.Entries = append(rep.Entries, entry)
		mu.Unlock()
		if err := write(); err != nil {
			errs = append(errs, err)
			break
		}
	}
	return errors.Join(append(errs, ln.Close())...)
}

// PipeProbeReport is a client's view of one connection.
type PipeProbeReport struct {
	Result  PipeResult `json:"result"`
	Claim   *Claim     `json:"claim,omitempty"`
	Verdict *Verdict   `json:"verdict,omitempty"`
}

// selfClaim is this process's own incarnation.
func selfClaim() (*Claim, error) {
	created, err := creationTime(windows.CurrentProcess())
	if err != nil {
		return nil, err
	}
	sid, err := processSID(windows.CurrentProcess())
	if err != nil {
		return nil, err
	}
	return &Claim{PID: windows.GetCurrentProcessId(), Created: created, SID: sid}, nil
}

// ProbePipe connects as the current token at Identification level. With
// openOnly it records only whether the open succeeded. With exitAfterSend
// it sends its claim and exits without waiting for the verdict.
func ProbePipe(name, client, claimMode string, openOnly, exitAfterSend bool) PipeProbeReport {
	rep := PipeProbeReport{Result: PipeResult{Client: client}}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	c, err := protocol.DialPipe(ctx, name)
	if err != nil {
		rep.Result.OpenError = win32Code(err)
		return rep
	}
	defer c.Close()
	rep.Result.Connected = true
	self, err := selfClaim()
	if err != nil {
		rep.Result.Reason = "own identity"
		return rep
	}
	rep.Result.PID, rep.Result.Created = self.PID, self.Created
	if openOnly {
		return rep
	}
	_ = c.SetDeadline(time.Now().Add(probeTimeout + settleWindow))
	r := bufio.NewReader(&limitedReader{c, 4096})
	if line, err := r.ReadString('\n'); err != nil || strings.TrimSpace(line) != "ready" {
		rep.Result.Reason = "no ready line"
		return rep
	}
	var claim *Claim
	line := []byte("{}")
	switch claimMode {
	case "self", "stale":
		claim = self
		if claimMode == "stale" {
			stale := *self
			stale.Created++
			claim = &stale
		}
		line, _ = json.Marshal(claim)
	case "malformed":
		line = []byte(`{"pid":"not a number"}`)
	case "silent":
		// Hang up without a request.
		return rep
	}
	rep.Claim = claim
	if _, err := c.Write(append(line, '\n')); err != nil {
		rep.Result.Reason = "send claim: " + err.Error()
		return rep
	}
	if exitAfterSend {
		os.Exit(0)
	}
	answer, err := r.ReadString('\n')
	if err != nil {
		rep.Result.Reason = "no verdict"
		return rep
	}
	var v Verdict
	if err := decodeStrict([]byte(answer), &v); err != nil {
		rep.Result.Reason = "malformed verdict"
		return rep
	}
	rep.Verdict = &v
	rep.Result.Accepted, rep.Result.Reason, rep.Result.Held = v.Accepted, v.Reason, v.Held
	return rep
}

func runProbePipe(args []string) (any, error) {
	fs := newFlags("probe-pipe")
	name := fs.String("name", "", "")
	fromConfig := fs.String("from-config", "", "")
	client := fs.String("client", "", "")
	claim := fs.String("claim", "self", "")
	openOnly := fs.Bool("open-only", false, "")
	exitAfterSend := fs.Bool("exit-after-send", false, "")
	out := fs.String("out", "", "")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if *fromConfig != "" {
		cfg, err := LoadProbeConfig(*fromConfig)
		if err != nil {
			return nil, err
		}
		if *name != "" || cfg.Pipe == "" {
			return nil, usage("--from-config needs a configured pipe and no --name")
		}
		*name = cfg.Pipe
	}
	if !strings.HasPrefix(*name, `\\.\pipe\`) || !controlPattern.MatchString(*client) {
		return nil, usage("--name must be a local pipe and --client a role")
	}
	switch *claim {
	case "self", "stale", "none", "malformed", "silent":
	default:
		return nil, usage("--claim must be self, stale, none, malformed or silent")
	}
	if err := absPath("out", *out); err != nil && !*exitAfterSend {
		return nil, err
	}
	rep := ProbePipe(*name, *client, *claim, *openOnly, *exitAfterSend)
	return rep, writeJSONFile(*out, rep)
}

// String identifies a verdict in logs without identities.
func (v Verdict) String() string {
	return fmt.Sprintf("accepted=%t reason=%s held=%t", v.Accepted, v.Reason, v.Held)
}
