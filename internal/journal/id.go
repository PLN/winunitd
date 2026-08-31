package journal

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

// EnvInvocationID is injected into the unit process on each start
// (DESIGN.md §24).
const EnvInvocationID = "WINUNIT_INVOCATION_ID"

// NewInvocationID returns a new RFC 4122 version-4 UUID for one unit
// start, including Restart= relaunch (DESIGN.md §24).
func NewInvocationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// ValidInvocationID reports whether id is an 8-4-4-4-12 hex UUID.
func ValidInvocationID(id string) bool {
	if len(id) != 36 {
		return false
	}
	if id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	for i, r := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' {
			continue
		}
		return false
	}
	return true
}

// InjectEnv sets WINUNIT_INVOCATION_ID on the process environment block.
func InjectEnv(env []string, id string) []string {
	if id == "" {
		return env
	}
	prefix := EnvInvocationID + "="
	kv := prefix + id
	for i, e := range env {
		name, _, _ := strings.Cut(e, "=")
		if strings.EqualFold(name, EnvInvocationID) {
			env[i] = kv
			return env
		}
	}
	return append(env, kv)
}
