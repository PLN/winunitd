package headless

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
)

// ProbeConfig names the targets of a workload's probes. The driver writes it
// into the workload's own state root before the case; commands name a probe,
// never a target, a credential or command text.
type ProbeConfig struct {
	// UnitFile is the workload's own unit definition (H13).
	UnitFile string `json:"unitFile,omitempty"`
	// PeerRoot is the other account's protected root (H13 negative).
	PeerRoot string `json:"peerRoot,omitempty"`
	// Absent and Denied are H14's missing and SYSTEM-owned paths.
	Absent string `json:"absent,omitempty"`
	Denied string `json:"denied,omitempty"`
	// Loopback is the workload's own echo listener, Peer the isolated peer's.
	Loopback string     `json:"loopback,omitempty"`
	Peer     string     `json:"peer,omitempty"`
	SMB      *SMBTarget `json:"smb,omitempty"`
	EFS      *EFSTarget `json:"efs,omitempty"`
	// Pipe is the SYSTEM qualification pipe the in-unit probe connects to.
	Pipe string `json:"pipe,omitempty"`
	// OwnDaemon, PeerDaemon and SystemDaemon are the data roots of the
	// account's own manager, the peer's and the system manager, for the
	// daemon-log denial probes.
	OwnDaemon    string `json:"ownDaemon,omitempty"`
	PeerDaemon   string `json:"peerDaemon,omitempty"`
	SystemDaemon string `json:"systemDaemon,omitempty"`
	// Winctl and StatusUnit let the workload read a unit's status from its
	// own manager, as the account, with no password logon.
	Winctl     string `json:"winctl,omitempty"`
	StatusUnit string `json:"statusUnit,omitempty"`
	// OutsideUnit is another unit of the account, which runs H15's
	// outside-unit pipe client.
	OutsideUnit string `json:"outsideUnit,omitempty"`
	// DenyPipes are H16's pipes, by client role, that the account must be
	// denied from inside its unit.
	DenyPipes map[string]string `json:"denyPipes,omitempty"`
}

// SMBTarget is the protected share's nonce file and its server.
type SMBTarget struct {
	Server       string `json:"server"`
	Path         string `json:"path"`
	ExpectSHA256 string `json:"expectSha256"`
}

// EFSTarget is the encrypted fixture and its unencrypted sibling.
type EFSTarget struct {
	Path         string `json:"path"`
	Plain        string `json:"plain"`
	ExpectSHA256 string `json:"expectSha256"`
}

func (c *ProbeConfig) validate() error {
	for _, p := range []string{c.UnitFile, c.PeerRoot, c.Absent, c.Denied, c.OwnDaemon, c.PeerDaemon, c.SystemDaemon, c.Winctl} {
		if p != "" && (!filepath.IsAbs(p) || filepath.Clean(p) != p) {
			return errors.New("probe paths must be clean absolute paths")
		}
	}
	for _, a := range []string{c.Loopback, c.Peer} {
		if a == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(a); err != nil {
			return fmt.Errorf("address %q", a)
		}
	}
	if c.Pipe != "" && !strings.HasPrefix(c.Pipe, `\\.\pipe\winunitd-qual\`) {
		return errors.New("the qualification pipe must be under the fixture's namespace")
	}
	for client, pipe := range c.DenyPipes {
		switch client {
		case ClientSystemOnly, ClientPeerUserPipe, ClientControlPipe, ClientMaintenancePipe:
		default:
			return errors.New("a denial pipe names an unknown client")
		}
		if !strings.HasPrefix(pipe, `\\.\pipe\`) {
			return errors.New("a denial pipe must be a local pipe")
		}
	}
	if c.Loopback != "" {
		host, _, _ := net.SplitHostPort(c.Loopback)
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return errors.New("the loopback address must be a loopback IP")
		}
	}
	if s := c.SMB; s != nil {
		if s.Server == "" || strings.ContainsAny(s.Server, `\/ `) || !strings.HasPrefix(s.Path, `\\`+s.Server+`\`) || !sha256Hex.MatchString(s.ExpectSHA256) {
			return errors.New("smb target needs a server, a UNC path on it and the expected hash")
		}
	}
	if e := c.EFS; e != nil {
		if !filepath.IsAbs(e.Path) || !filepath.IsAbs(e.Plain) || !sha256Hex.MatchString(e.ExpectSHA256) {
			return errors.New("efs target needs absolute paths and the expected hash")
		}
	}
	return nil
}

// LoadProbeConfig strictly reads a probe configuration.
func LoadProbeConfig(path string) (*ProbeConfig, error) {
	data, err := readBounded(path)
	if err != nil {
		return nil, err
	}
	var c ProbeConfig
	if err := decodeStrict(data, &c); err != nil {
		return nil, fmt.Errorf("probe config: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// ProbeReport is one probe's output: what it found and the token of the
// process that ran it.
type ProbeReport struct {
	Probe    string       `json:"probe"`
	Time     string       `json:"time"`
	Identity *TokenProbe  `json:"identity,omitempty"`
	TCP      *TCPResult   `json:"tcp,omitempty"`
	SMB      *SMBResult   `json:"smb,omitempty"`
	EFS      *EFSResult   `json:"efs,omitempty"`
	Paths    []PathResult `json:"paths,omitempty"`
}
