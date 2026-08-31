//go:build !windows

package timers

import (
	"os"
	"strconv"
	"strings"
	"time"
)

func platformSinceBoot() time.Duration {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	sec, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || sec < 0 {
		return 0
	}
	return time.Duration(sec * float64(time.Second))
}
