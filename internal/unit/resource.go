package unit

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// parseMemoryMax parses MemoryMax= (DESIGN.md §43). A bare number is bytes.
// Suffixes K, M, and G are 1024-based (for example 2G). Zero and unknown
// suffixes are errors.
func parseMemoryMax(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty MemoryMax")
	}
	i := 0
	if s[0] == '+' {
		i++
	}
	if i < len(s) && s[i] == '-' {
		return 0, fmt.Errorf("invalid MemoryMax %q", s)
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == start {
		return 0, fmt.Errorf("invalid MemoryMax %q", s)
	}
	num := s[start:i]
	suf := strings.TrimSpace(s[i:])
	n, err := strconv.ParseUint(num, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid MemoryMax %q", s)
	}
	var mul uint64 = 1
	switch strings.ToUpper(suf) {
	case "":
		mul = 1
	case "K":
		mul = 1024
	case "M":
		mul = 1024 * 1024
	case "G":
		mul = 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("invalid MemoryMax %q", s)
	}
	if n == 0 {
		return 0, fmt.Errorf("invalid MemoryMax %q", s)
	}
	if mul > 1 && n > math.MaxUint64/mul {
		return 0, fmt.Errorf("invalid MemoryMax %q", s)
	}
	return n * mul, nil
}

// parseProcessLimit parses ProcessLimit=. Zero and negative values are errors.
func parseProcessLimit(s string) (uint32, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty ProcessLimit")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid ProcessLimit %q", s)
	}
	if n <= 0 {
		return 0, fmt.Errorf("invalid ProcessLimit %q", s)
	}
	if n > math.MaxUint32 {
		return 0, fmt.Errorf("invalid ProcessLimit %q", s)
	}
	return uint32(n), nil
}

// parsePriorityClass parses PriorityClass=. realtime is rejected.
func parsePriorityClass(s string) (PriorityClass, error) {
	raw := strings.TrimSpace(s)
	v := PriorityClass(strings.ToLower(raw))
	switch v {
	case PriorityIdle, PriorityBelowNormal, PriorityNormal, PriorityAboveNormal, PriorityHigh:
		return v, nil
	case "":
		return "", fmt.Errorf("empty PriorityClass")
	default:
		return "", fmt.Errorf("invalid PriorityClass %q (supported: idle, below-normal, normal, above-normal, high)", raw)
	}
}

// parseCPUWeight parses CPUWeight= (DESIGN.md §43 R2). Integer 1–10000.
func parseCPUWeight(s string) (uint32, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty CPUWeight")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid CPUWeight %q", s)
	}
	if n < 1 || n > 10000 {
		return 0, fmt.Errorf("invalid CPUWeight %q", s)
	}
	return uint32(n), nil
}

// parseCPUQuota parses CPUQuota= N% (DESIGN.md §43 R2). N is an integer 1–10000.
// The trailing % is required.
func parseCPUQuota(s string) (uint32, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty CPUQuota")
	}
	if !strings.HasSuffix(s, "%") {
		return 0, fmt.Errorf("invalid CPUQuota %q", s)
	}
	num := strings.TrimSpace(s[:len(s)-1])
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid CPUQuota %q", s)
	}
	if n < 1 || n > 10000 {
		return 0, fmt.Errorf("invalid CPUQuota %q", s)
	}
	return uint32(n), nil
}

// parseIoPriority parses IoPriority=. idle, low, normal, high only.
func parseIoPriority(s string) (IoPriority, error) {
	raw := strings.TrimSpace(s)
	v := IoPriority(strings.ToLower(raw))
	switch v {
	case IoIdle, IoLow, IoNormal, IoHigh:
		return v, nil
	case "":
		return "", fmt.Errorf("empty IoPriority")
	default:
		return "", fmt.Errorf("invalid IoPriority %q (supported: idle, low, normal, high)", raw)
	}
}
