package headless

import (
	"errors"
	"strings"
	"time"
)

// Observer bounds.
const (
	maxObserveDuration = 3 * time.Hour
	defaultScan        = 50 * time.Millisecond
	maxCrashLives      = 64
)

// ObserveConfig is the observer's validated command line.
type ObserveConfig struct {
	Report        string
	Admission     string
	DaemonImage   string
	WorkloadImage string
	// Accounts maps the account roles to watch to their SIDs.
	Accounts map[string]string
	Duration time.Duration
	Scan     time.Duration
	// StopFile ends the observation when it appears; Marks is a directory
	// whose new entries the observer timestamps as marks.
	StopFile string
	Marks    string
	// Target is the account role whose processes the observer crashes and
	// whose failures release the workload. The broker has no account.
	Target string
	Crash  string
	// Lives is how long each generation of the crash role may live, in
	// creation order, before the observer terminates it; Rest applies to
	// generations beyond Lives, and zero means they are not crashed.
	Lives []time.Duration
	Rest  time.Duration
	// CrashClass, when set, limits the crash plan to generations under
	// that token class (s4u or wts), so an account's other manager is
	// left alone.
	CrashClass string
	// The observer creates ReleaseFile once it has seen ReleaseAfter failed
	// workload generations of the target.
	ReleaseFile  string
	ReleaseAfter int
}

// Plan is the config as the report records it.
func (c ObserveConfig) Plan() ObserverPlan {
	p := ObserverPlan{Target: c.Target, Crash: c.Crash, CrashClass: c.CrashClass, ReleaseAfter: c.ReleaseAfter}
	for _, l := range c.Lives {
		p.Lives = append(p.Lives, l.String())
	}
	if c.Crash != "" {
		p.Rest = c.Rest.String()
	}
	return p
}

// LifeOf is how long the index-th generation of the crash role may live,
// and whether it is crashed at all.
func (c ObserveConfig) LifeOf(index int) (time.Duration, bool) {
	if index < len(c.Lives) {
		return c.Lives[index], true
	}
	return c.Rest, c.Rest > 0
}

func parseObserve(args []string) (ObserveConfig, error) {
	fs := newFlags("observe")
	var c ObserveConfig
	var accounts, lives listFlag
	fs.StringVar(&c.Report, "report", "", "")
	fs.StringVar(&c.Admission, "admission", "", "")
	fs.StringVar(&c.DaemonImage, "daemon-image", "", "")
	fs.StringVar(&c.WorkloadImage, "workload-image", "", "")
	fs.Var(&accounts, "account", "")
	fs.DurationVar(&c.Duration, "duration", 0, "")
	fs.DurationVar(&c.Scan, "scan", defaultScan, "")
	fs.StringVar(&c.StopFile, "stop-file", "", "")
	fs.StringVar(&c.Marks, "marks", "", "")
	fs.StringVar(&c.Target, "target", "", "")
	fs.StringVar(&c.Crash, "crash", "", "")
	fs.Var(&lives, "crash-lives", "")
	fs.DurationVar(&c.Rest, "crash-rest", 0, "")
	fs.StringVar(&c.CrashClass, "crash-class", "", "")
	fs.StringVar(&c.ReleaseFile, "release-file", "", "")
	fs.IntVar(&c.ReleaseAfter, "release-after", 0, "")
	if err := parse(fs, args); err != nil {
		return c, err
	}
	var errs []error
	for name, p := range map[string]string{"report": c.Report, "admission": c.Admission, "daemon-image": c.DaemonImage, "workload-image": c.WorkloadImage} {
		errs = append(errs, absPath(name, p))
	}
	for name, p := range map[string]string{"stop-file": c.StopFile, "marks": c.Marks, "release-file": c.ReleaseFile} {
		if p != "" {
			errs = append(errs, absPath(name, p))
		}
	}
	errs = append(errs, durationIn("duration", c.Duration, time.Second, maxObserveDuration),
		durationIn("scan", c.Scan, 10*time.Millisecond, MaxObservationGap/2))
	c.Accounts = map[string]string{}
	for _, a := range accounts {
		role, sid, ok := strings.Cut(a, "=")
		if !ok || role != AccountA && role != AccountB && role != AccountAdmin || !sidPattern.MatchString(sid) || sid == SystemSID || c.Accounts[role] != "" {
			errs = append(errs, usage("--account must be A=SID, B=SID or admin=SID, once each"))
			continue
		}
		for _, other := range c.Accounts {
			if other == sid {
				errs = append(errs, usage("both accounts have one SID"))
			}
		}
		c.Accounts[role] = sid
	}
	if len(c.Accounts) == 0 {
		errs = append(errs, usage("--account is required"))
	}
	if c.Target != "" && c.Accounts[c.Target] == "" {
		errs = append(errs, usage("--target must be a watched account"))
	}
	switch c.Crash {
	case "":
		if len(lives) > 0 || c.Rest != 0 {
			errs = append(errs, usage("crash lives need --crash"))
		}
	case RoleBroker:
	case RoleManager, RoleWorkload:
		if c.Target == "" {
			errs = append(errs, usage("--crash %s needs --target", c.Crash))
		}
	default:
		errs = append(errs, usage("--crash must be broker, manager or workload"))
	}
	if c.CrashClass != "" && (c.CrashClass != SourceS4U && c.CrashClass != SourceWTS || c.Crash == "" || c.Crash == RoleBroker) {
		errs = append(errs, usage("--crash-class must be s4u or wts, for an account's crash plan"))
	}
	if len(lives) > maxCrashLives {
		errs = append(errs, usage("at most %d crash lives", maxCrashLives))
	}
	for _, l := range lives {
		d, err := time.ParseDuration(l)
		if err != nil || d < 0 || d > time.Hour {
			errs = append(errs, usage("crash life %q", l))
			continue
		}
		c.Lives = append(c.Lives, d)
	}
	if c.Crash != "" && len(c.Lives) == 0 && c.Rest == 0 {
		errs = append(errs, usage("--crash needs --crash-lives or --crash-rest"))
	}
	if c.Rest < 0 || c.Rest > time.Hour {
		errs = append(errs, usage("--crash-rest"))
	}
	if (c.ReleaseFile == "") != (c.ReleaseAfter == 0) || c.ReleaseAfter < 0 || c.ReleaseAfter > 1000 {
		errs = append(errs, usage("--release-file and --release-after go together; at most 1000 failures"))
	}
	if c.ReleaseFile != "" && c.Target == "" {
		errs = append(errs, usage("--release-file needs --target"))
	}
	return c, errors.Join(errs...)
}
