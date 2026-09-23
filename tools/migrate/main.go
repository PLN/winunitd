// Command migrate is the R6.5 operator migration tool.
// It discovers a disposable pilot fixture, backs it up, dry-runs, and
// either refuses conflicts or hands ownership to the product service.
// It is not an MSI custom action and it does not call winunitd install
// or winunitd uninstall. The caller supplies the MSI path.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

const usageText = `migrate — winunitd R6.5 pilot migration

Usage:
  migrate -root DIR init -scenario NAME
  migrate -root DIR discover
  migrate -root DIR backup -out DIR
  migrate -root DIR dry-run -msi PATH
  migrate -root DIR apply -msi PATH -backup DIR [-fail-after PHASE] [-linger] [-execute-msi]
  migrate -root DIR health [-backup DIR]
  migrate -root DIR rollback -backup DIR

The fixture root is a disposable stand-in for a Task Scheduler pilot.
Accounts in that fixture are alice, bob, and carol. -user=false limits
discovery to machine-wide MSI preflight rejects. Linger is copied only
with -linger. -execute-msi invokes msiexec on Windows; the default
records the caller-supplied package without embedding one.

Phases for -fail-after are disable, stop, copy, install, and health.
A failed apply restores the backup and keeps that backup.
-root, -user, -linger, and -execute-msi come before the command.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "fixture root")
	user := fs.Bool("user", true, "include per-user discovery")
	execute := fs.Bool("execute-msi", false, "invoke msiexec with the caller-supplied package")
	linger := fs.Bool("linger", false, "copy linger records")
	fs.Usage = func() { fmt.Fprint(stderr, usageText) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 || fs.Arg(0) == "" {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	if *root == "" {
		fmt.Fprintln(stderr, "migrate: fixture root is required")
		return 2
	}
	sub := flag.NewFlagSet(fs.Arg(0), flag.ContinueOnError)
	sub.SetOutput(stderr)
	msi := sub.String("msi", "", "caller-supplied MSI path")
	backupDir := sub.String("backup", "", "backup directory from the backup command")
	out := sub.String("out", "", "backup directory to create")
	failAfter := sub.String("fail-after", "", "injected failure phase")
	scenario := sub.String("scenario", "", "fixture scenario for init")
	sub.Usage = func() { fmt.Fprint(stderr, usageText) }
	if err := sub.Parse(fs.Args()[1:]); err != nil {
		return 2
	}
	if sub.NArg() != 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	opt := options{
		UserContext: *user,
		Linger:      *linger,
		MSIPath:     *msi,
		ExecuteMSI:  *execute,
		FailAfter:   *failAfter,
		BackupDir:   *backupDir,
	}
	switch fs.Arg(0) {
	case "init":
		if err := initFixture(*root, *scenario); err != nil {
			return fail(stderr, err)
		}
		return writeOK(stdout, report{Command: "init"})
	case "discover":
		return discoverCmd(*root, opt, false, stdout, stderr)
	case "dry-run":
		return discoverCmd(*root, opt, true, stdout, stderr)
	case "backup":
		if err := backup(*root, *out); err != nil {
			return fail(stderr, err)
		}
		return writeOK(stdout, report{Command: "backup"})
	case "apply":
		err := apply(*root, opt)
		if errors.Is(err, errBlocked) {
			return discoverCmd(*root, opt, true, stdout, stderr)
		}
		if err != nil {
			return fail(stderr, err)
		}
		fx, loadErr := loadFixture(*root)
		if loadErr != nil {
			return fail(stderr, loadErr)
		}
		checks, healthErr := evaluateHealth(*root, fx, opt)
		if healthErr != nil {
			return fail(stderr, healthErr)
		}
		return writeOK(stdout, report{Command: "apply", Health: checks})
	case "health":
		fx, err := loadFixture(*root)
		if err != nil {
			return fail(stderr, err)
		}
		checks, err := evaluateHealth(*root, fx, opt)
		if err != nil {
			return fail(stderr, err)
		}
		return writeOK(stdout, report{Command: "health", Health: checks})
	case "rollback":
		if err := rollback(*root, *backupDir); err != nil {
			return fail(stderr, err)
		}
		return writeOK(stdout, report{Command: "rollback", Restored: true})
	default:
		fmt.Fprint(stderr, usageText)
		return 2
	}
}

func discoverCmd(root string, opt options, actions bool, stdout, stderr io.Writer) int {
	fx, err := loadFixture(root)
	if err != nil {
		return fail(stderr, err)
	}
	rep, err := plan(root, fx, opt, actions)
	if err != nil {
		return fail(stderr, err)
	}
	if actions {
		rep.Command = "dry-run"
	} else {
		rep.Command = "discover"
	}
	if err := writeReport(stdout, rep); err != nil {
		return fail(stderr, err)
	}
	if rep.Blocked {
		return 2
	}
	return 0
}

func writeOK(stdout io.Writer, rep report) int {
	if err := writeReport(stdout, rep); err != nil {
		fmt.Fprintln(os.Stderr, "migrate: write report")
		return 1
	}
	return 0
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "migrate: %v\n", err)
	return 1
}
