package notify

import (
	"strings"
	"time"
)

// Inject adds WINUNIT_NOTIFY_PIPE and, when watchdog > 0, WINUNIT_WATCHDOG_USEC.
func Inject(env []string, pipe string, watchdog time.Duration) []string {
	if pipe == "" {
		return env
	}
	env = setEnv(env, EnvNotifyPipe, pipe)
	if watchdog > 0 {
		env = setEnv(env, EnvWatchdogUsec, WatchdogUsec(watchdog.Microseconds()))
	}
	return env
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	kv := prefix + value
	for i, e := range env {
		if len(e) >= len(prefix) && equalFoldASCII(e[:len(key)], key) && (len(e) == len(key) || e[len(key)] == '=') {
			env[i] = kv
			return env
		}
	}
	return append(env, kv)
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return strings.EqualFold(a, b)
}

// LookupEnv returns the value of key in a KEY=value block.
func LookupEnv(env []string, key string) (string, bool) {
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if ok && strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}
