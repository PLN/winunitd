package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/servicing"
)

const floorUsage = `Usage:
  winunitd floor show  [--base-dir DIR]
  winunitd floor check [--base-dir DIR]
  winunitd floor set   [--base-dir DIR] [--min-version VERSION] [--require NAME[,NAME]] [--require-clean]
  winunitd floor clear [--base-dir DIR]

The compatibility floor is the minimum build that may admit hosted work. It
is stored in DIR\daemon\compat-floor.json (default %ProgramData%\winunitd),
writable only by SYSTEM and Administrators. Run these verbs as an
administrator with the installed winunitd.exe: check and set evaluate this
binary, the build the manager runs.

  show   Print the floor, or report that none is set.
  check  Exit 0 when this build satisfies the floor or none is set, 1 when
         it is below the floor or the record cannot be trusted.
  set    Write a floor. Refused (exit 1) unless this build satisfies it.
         A floor that raises any requirement is written only while the
         system manager is stopped: the winunitd service stopped with no
         process, its endpoints unserved and no other winunitd.exe running.
  clear  Remove the floor. Lowering or clearing applies at the next start.

To raise the floor before admitting a workload that needs it:
  1. winctl maintenance          quiesce system and user work
  2. stop the winunitd service and wait for its process to exit
  3. winunitd floor set ...       checks the stop before and after writing
  4. start the service; winunitd floor check and winctl status must show
     no admission hold
  5. admit the workload

A manager below the floor keeps its control endpoint but starts no units and
launches no user managers until it restarts.
`

// featureList accepts repeated or comma-separated --require values.
type featureList []string

func (f *featureList) String() string { return strings.Join(*f, ",") }

func (f *featureList) Set(v string) error {
	for _, name := range strings.Split(v, ",") {
		if name = strings.TrimSpace(name); name != "" {
			*f = append(*f, name)
		}
	}
	return nil
}

// floorEnv is what the floor verbs evaluate: this binary's identity and the
// check that the system manager and the managers it brokers are stopped.
type floorEnv struct {
	build   servicing.Build
	stopped func() error
}

func runFloor(args []string, stdout, stderr io.Writer, env floorEnv) int {
	build := env.build
	if len(args) == 0 {
		fmt.Fprint(stderr, floorUsage)
		return 2
	}
	verb := args[0]
	switch verb {
	case "-h", "-help", "--help":
		fmt.Fprint(stdout, floorUsage)
		return 0
	}
	fs := flag.NewFlagSet("winunitd floor "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, floorUsage) }
	baseDir := fs.String("base-dir", manager.DefaultBaseDir(), "data directory")
	var minVersion string
	var features featureList
	var clean bool
	if verb == "set" {
		fs.StringVar(&minVersion, "min-version", "", "lowest admitted release")
		fs.Var(&features, "require", "required capability feature")
		fs.BoolVar(&clean, "require-clean", false, "require a known, unmodified source revision")
	}
	switch verb {
	case "show", "check", "set", "clear":
	default:
		fmt.Fprintf(stderr, "winunitd floor: unknown verb %q\n", verb)
		fmt.Fprint(stderr, floorUsage)
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "winunitd floor: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	path := servicing.FloorPath(*baseDir)
	switch verb {
	case "show":
		f, err := servicing.ReadFloor(path)
		if err != nil {
			fmt.Fprintf(stderr, "winunitd floor: %v\n", err)
			return 1
		}
		if f == nil {
			fmt.Fprintln(stdout, "no compatibility floor is set")
			return 0
		}
		data, err := servicing.EncodeFloor(f)
		if err != nil {
			fmt.Fprintf(stderr, "winunitd floor: %v\n", err)
			return 1
		}
		_, _ = stdout.Write(data)
		return 0
	case "check":
		if hold := servicing.AdmissionHold(path, build); hold != "" {
			fmt.Fprintf(stdout, "%s: %s\n", describeBuild(build), hold)
			return 1
		}
		fmt.Fprintf(stdout, "%s: admission allowed\n", describeBuild(build))
		return 0
	case "set":
		f := &servicing.Floor{Schema: servicing.FloorSchema, MinVersion: minVersion, RequireFeatures: features, RequireCleanBuild: clean}
		if err := f.Validate(); err != nil {
			fmt.Fprintf(stderr, "winunitd floor: %v\n", err)
			return 2
		}
		if v := servicing.Evaluate(f, build); !v.Satisfied {
			fmt.Fprintf(stderr, "winunitd floor: refused: %s does not satisfy it: %s\n", describeBuild(build), strings.Join(v.Reasons, "; "))
			return 1
		}
		// A running manager sampled the floor when it started and keeps
		// admitting work under it, so a raise needs that manager stopped.
		// An unusable current record counts as no floor: replacing it raises.
		prev, _ := servicing.ReadFloor(path)
		raise := !f.Within(prev)
		if raise {
			if err := env.stopped(); err != nil {
				fmt.Fprintf(stderr, "winunitd floor: refused: raising the floor needs the system manager stopped: %v; run winctl maintenance, stop the winunitd service and every winunitd process, then retry\n", err)
				return 1
			}
		}
		if err := servicing.WriteFloor(path, f); err != nil {
			fmt.Fprintf(stderr, "winunitd floor: %v\n", err)
			return 1
		}
		if raise {
			if err := env.stopped(); err != nil {
				fmt.Fprintf(stderr, "winunitd floor: the floor was raised, but a manager started during the change (%v) and may admit work under the previous floor; restart it before admitting work that needs this floor\n", err)
				return 1
			}
			fmt.Fprintln(stdout, "compatibility floor raised; start the service and confirm that winunitd floor check and winctl status show no admission hold before admitting work that needs it")
			return 0
		}
		fmt.Fprintln(stdout, "compatibility floor written without raising it; it applies at the next manager start")
		return 0
	default: // clear
		if err := servicing.RemoveFloor(path); err != nil {
			fmt.Fprintf(stderr, "winunitd floor: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "compatibility floor removed; restart the manager to reopen held admission")
		return 0
	}
}

func describeBuild(b servicing.Build) string {
	s := "winunitd " + b.Version
	switch {
	case b.Commit == "":
		return s + " (no source revision)"
	case b.Modified == nil:
		return s + " commit " + b.Commit + " (source state unknown)"
	case *b.Modified:
		return s + " commit " + b.Commit + " (modified)"
	}
	return s + " commit " + b.Commit
}
