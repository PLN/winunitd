package notify

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Send writes one notify payload and closes the connection.
func Send(ctx context.Context, addr string, msg Message) error {
	body := Format(msg)
	if body == "" {
		return fmt.Errorf("empty notify payload")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	conn, err := Dial(ctx, addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(d)
	}
	_, err = io.WriteString(conn, body)
	return err
}

// SendRetry dials until ctx is done. Used by tests and the helper process.
func SendRetry(ctx context.Context, addr string, msg Message) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var last error
	for {
		last = Send(ctx, addr, msg)
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			if last != nil {
				return last
			}
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// PipeFromEnv reads WINUNIT_NOTIFY_PIPE.
func PipeFromEnv() (string, error) {
	return PipeFromGetenv(os.Getenv)
}

// PipeFromGetenv reads WINUNIT_NOTIFY_PIPE via getenv.
func PipeFromGetenv(getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	addr := strings.TrimSpace(getenv(EnvNotifyPipe))
	if addr == "" {
		return "", fmt.Errorf("%s is not set", EnvNotifyPipe)
	}
	return addr, nil
}
