package headless

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The echo protocol is one line: the nonce. The server answers the same
// line and records the nonce it received. Nothing else is accepted.
var noncePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// maxEchoReceipts bounds an echo server's report.
const maxEchoReceipts = 1024

// EchoReport is an echo server's record of the nonces it received.
type EchoReport struct {
	Listen   string   `json:"listen"`
	Received []string `json:"received"`
	Rejected int      `json:"rejected"`
}

// EchoServer answers nonce lines on one listener and records them.
type EchoServer struct {
	ln     net.Listener
	mu     sync.Mutex
	report EchoReport
	wg     sync.WaitGroup
}

// ListenEcho starts an echo server on addr.
func ListenEcho(addr string) (*EchoServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &EchoServer{ln: ln, report: EchoReport{Listen: ln.Addr().String()}}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// Addr is the listener's address.
func (s *EchoServer) Addr() string { return s.ln.Addr().String() }

func (s *EchoServer) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.answer(c)
		}()
	}
}

func (s *EchoServer) answer(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(&limitedReader{c, 64}).ReadString('\n')
	nonce := strings.TrimSpace(line)
	s.mu.Lock()
	ok := err == nil && noncePattern.MatchString(nonce) && len(s.report.Received) < maxEchoReceipts
	if ok {
		s.report.Received = append(s.report.Received, nonce)
	} else {
		s.report.Rejected++
	}
	s.mu.Unlock()
	if ok {
		_, _ = c.Write([]byte(nonce + "\n"))
	}
}

// Report returns a copy of what the server received.
func (s *EchoServer) Report() EchoReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.report
	r.Received = append([]string(nil), s.report.Received...)
	return r
}

// Close stops the server and waits for its connections.
func (s *EchoServer) Close() error {
	err := s.ln.Close()
	s.wg.Wait()
	return err
}

type limitedReader struct {
	c net.Conn
	n int
}

func (r *limitedReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, errors.New("line too long")
	}
	if len(p) > r.n {
		p = p[:r.n]
	}
	n, err := r.c.Read(p)
	r.n -= n
	return n, err
}

// EchoRoundTrip sends nonce to addr and checks the echo, within timeout,
// recording the Winsock or system error code of a failed step.
func EchoRoundTrip(target, addr, nonce string, timeout time.Duration) TCPResult {
	r := TCPResult{Target: target, Nonce: nonce}
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		r.Error = socketError(err)
		return r
	}
	defer c.Close()
	r.Connected = true
	_ = c.SetDeadline(time.Now().Add(timeout))
	n, err := c.Write([]byte(nonce + "\n"))
	r.Sent = n
	if err != nil {
		r.Error = socketError(err)
		return r
	}
	line, err := bufio.NewReader(&limitedReader{c, 64}).ReadString('\n')
	r.Received = len(line)
	if err != nil {
		r.Error = socketError(err)
		return r
	}
	r.Echoed = strings.TrimSpace(line) == nonce
	return r
}

func runEchoServe(args []string) error {
	fs := newFlags("echo-serve")
	listen := fs.String("listen", "", "")
	report := fs.String("report", "", "")
	duration := fs.Duration("duration", 5*time.Minute, "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if _, _, err := net.SplitHostPort(*listen); err != nil {
		return usage("--listen must be HOST:PORT")
	}
	if err := errors.Join(absPath("report", *report), durationIn("duration", *duration, time.Second, time.Hour)); err != nil {
		return err
	}
	s, err := ListenEcho(*listen)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(*duration)
	for time.Now().Before(deadline) {
		if err := writeJSONFile(*report, s.Report()); err != nil {
			_ = s.Close()
			return err
		}
		time.Sleep(time.Second)
	}
	err = s.Close()
	return errors.Join(err, writeJSONFile(*report, s.Report()))
}

// writeJSONFile atomically replaces path with v, flushed to disk.
func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > MaxFileBytes {
		return fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), MaxFileBytes)
	}
	tmp := fmt.Sprintf("%s.tmp-%d", path, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return baseOnly(err)
	}
	_, werr := f.Write(append(data, '\n'))
	serr := f.Sync()
	if err := errors.Join(werr, serr, f.Close()); err != nil {
		_ = os.Remove(tmp)
		return baseOnly(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := os.Rename(tmp, path)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			_ = os.Remove(tmp)
			return baseOnly(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
