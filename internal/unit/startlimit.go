package unit

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// systemd-shaped defaults for omitted [Unit] StartLimitIntervalSec= /
// StartLimitBurst= (DESIGN.md §20).
const (
	DefaultStartLimitInterval = 10 * time.Second
	DefaultStartLimitBurst    = 5
)

func parseStartLimitBurst(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty StartLimitBurst")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid StartLimitBurst %q", s)
	}
	return n, nil
}
