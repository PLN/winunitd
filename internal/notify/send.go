package notify

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// acceptanceBanner confirms that the server has accepted the connection. In
// particular, a Windows pipe client must not close before ConnectNamedPipe
// finishes or the listener can discard the connection with ERROR_NO_DATA.
const acceptanceBanner = "WINUNITD-NOTIFY/1\n"

// Send waits for server acceptance, writes one notify payload, and closes the
// connection. Acceptance is not an acknowledgement of application readiness.
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
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(d)
	}
	banner := make([]byte, len(acceptanceBanner))
	if _, err := io.ReadFull(conn, banner); err != nil {
		return fmt.Errorf("notify acceptance: %w", err)
	}
	if string(banner) != acceptanceBanner {
		return fmt.Errorf("unsupported notify acceptance banner")
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
