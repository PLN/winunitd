//go:build windows

package headless

import (
	"encoding/json"
	"errors"
	"io"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func runWindowsRole(role string, args []string, stdout io.Writer) error {
	var rep any
	var err error
	switch role {
	case "serve":
		return runServe(args)
	case "fail":
		return runFail(args)
	case "pipe-serve":
		return runPipeServe(args)
	case "probe-pipe":
		rep, err = runProbePipe(args)
	default:
		rep, err = runProbe(role, args)
	}
	if rep != nil {
		_ = json.NewEncoder(stdout).Encode(rep)
	}
	return err
}

// runProbe runs probe-token, probe-path, probe-tcp, probe-smb or probe-efs
// and writes its report.
func runProbe(role string, args []string) (any, error) {
	fs := newFlags(role)
	out := fs.String("out", "", "")
	config := fs.String("config", "", "")
	nonce := fs.String("nonce", "", "")
	set := fs.String("set", "", "")
	state := fs.String("state", "", "")
	target := fs.String("target", "", "")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if err := absPath("out", *out); err != nil {
		return nil, err
	}
	if *nonce == "" {
		*nonce = newNonce()
	}
	if !noncePattern.MatchString(*nonce) {
		return nil, usage("--nonce must be 32 hexadecimal digits")
	}
	var cfg *ProbeConfig
	if role != "probe-token" {
		if err := absPath("config", *config); err != nil {
			return nil, err
		}
		var err error
		if cfg, err = LoadProbeConfig(*config); err != nil {
			return nil, err
		}
	}
	id, err := ProbeOwnToken()
	if err != nil {
		return nil, err
	}
	rep := ProbeReport{Probe: role, Time: time.Now().UTC().Format(time.RFC3339Nano), Identity: &id}
	switch role {
	case "probe-token":
	case "probe-path":
		if *set != PathsOwnRoots && *set != PathsMissingAndDenied {
			return nil, usage("--set must be %s or %s", PathsOwnRoots, PathsMissingAndDenied)
		}
		if *state == "" {
			if *state, err = defaultStateRoot(); err != nil {
				return nil, err
			}
		}
		rep.Paths = ProbePaths(*set, cfg, *state, *nonce)
	case "probe-tcp":
		addr := map[string]string{"loopback": cfg.Loopback, "peer": cfg.Peer}[*target]
		if addr == "" {
			return nil, usage("--target must be a configured loopback or peer")
		}
		r := EchoRoundTrip(*target, addr, *nonce, probeTimeout)
		rep.TCP = &r
	case "probe-smb":
		if cfg.SMB == nil {
			return nil, usage("no smb target configured")
		}
		r := ProbeSMB(cfg.SMB, *nonce)
		rep.SMB = &r
	case "probe-efs":
		if cfg.EFS == nil {
			return nil, usage("no efs target configured")
		}
		r := ProbeEFS(cfg.EFS)
		rep.EFS = &r
	}
	return rep, writeJSONFile(*out, rep)
}

// securityFingerprint is the file's owner, group and DACL in SDDL.
func securityFingerprint(path string) (string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	return sd.String(), nil
}

func socketError(err error) uint32 {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return uint32(errno)
	}
	return uint32(windows.ERROR_GEN_FAILURE)
}
