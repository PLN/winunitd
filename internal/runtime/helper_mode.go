package runtime

import "strings"

// winunitdHelperArgPrefix is passed as ExecStart argv / Task Scheduler
// Arguments so TestMain can run a helper before m.Run() (which would reject
// the unknown flag).
const winunitdHelperArgPrefix = "-winunitd-helper="

func helperModeFrom(args []string, env string) string {
	for _, a := range args {
		i := strings.Index(a, winunitdHelperArgPrefix)
		if i < 0 {
			continue
		}
		rest := a[i+len(winunitdHelperArgPrefix):]
		if j := strings.IndexAny(rest, " \t"); j >= 0 {
			rest = rest[:j]
		}
		if rest != "" {
			return rest
		}
	}
	return strings.TrimSpace(env)
}

// HelperModeFrom reports the -winunitd-helper= value for TestMain. It inspects
// every os.Args token (including argv[0]) and takes the first token after the
// prefix so a glued "-winunitd-helper=sleep -test.run=^$" still matches.
func HelperModeFrom(args []string, env string) string {
	return helperModeFrom(args, env)
}
