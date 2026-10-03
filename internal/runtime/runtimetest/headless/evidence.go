package headless

import (
	"math"
	"sort"
	"time"
)

// Evidence is a record's raw observations. The summary recomputes every
// metric, characterization and sub-result verdict from it; a record's own
// claims about them are not trusted. Lifecycle values (launches, negative
// windows, replacements) come only from the observer's report.
type Evidence struct {
	Observer *ObserverReport `json:"observer,omitempty"`
	// Status is a unit status snapshot from winctl.
	Status      *UnitStatusProof `json:"status,omitempty"`
	FirstUse    *FirstUseProof   `json:"firstUse,omitempty"`
	TestRun     *TestRunProof    `json:"testRun,omitempty"`
	Endpoints   []EndpointHealth `json:"endpoints,omitempty"`
	PipeServers []ServerReport   `json:"pipeServers,omitempty"`
	TCP         []TCPResult      `json:"tcp,omitempty"`
	SMB         *SMBResult       `json:"smb,omitempty"`
	EFS         *EFSResult       `json:"efs,omitempty"`
	Paths       []PathResult     `json:"paths,omitempty"`
	Pipe        []PipeResult     `json:"pipe,omitempty"`
	Op          *OpResult        `json:"op,omitempty"`
	Server      *Principal       `json:"server,omitempty"`
	Receipt     *EchoReceipt     `json:"receipt,omitempty"`
	Token       *TokenProbe      `json:"tokenProbe,omitempty"`
	Notes       []string         `json:"notes,omitempty"`
	Native      []NativeError    `json:"native,omitempty"`
}

// NativeError is an operation's exact native result.
type NativeError struct {
	Op    string `json:"op"`
	Win32 uint32 `json:"win32"`
}

// metrics computes every metric the derived lifecycle and the status
// snapshot support. A metric that they cannot support is absent, which fails
// any requirement on it.
func metrics(ev Lifecycle, status *UnitStatusProof, capSec float64) map[string]float64 {
	out := map[string]float64{}
	att := append([]Attempt(nil), ev.Attempts...)
	sort.SliceStable(att, func(i, j int) bool { return att[i].Launched.Before(att[j].Launched) })
	if len(att) > 0 {
		out["durationSec"] = att[len(att)-1].Launched.Sub(att[0].Launched).Seconds()
		failures := 0
		for _, a := range att {
			if !a.Exited.IsZero() && a.ExitCode != 0 {
				failures++
			}
		}
		out["failures"] = float64(failures)
		out["maxStartsIn10s"] = float64(maxInWindow(att, 10*time.Second))
		longest := 0.0
		for _, a := range att {
			if !a.Exited.IsZero() {
				longest = math.Max(longest, a.Exited.Sub(a.Launched).Seconds())
			}
		}
		out["maxExitedLifeSec"] = longest
		// Recovered: the last launch still runs at the end of the
		// observation and was not terminated by the observer.
		last := att[len(att)-1]
		out["recovered"] = boolMetric(last.Exited.IsZero() && !last.Crashed)
	}
	gaps := make([]float64, 0, len(att))
	for i := 1; i < len(att); i++ {
		gaps = append(gaps, att[i].Launched.Sub(att[i-1].Launched).Seconds())
	}
	if capSec > 0 && len(gaps) > 0 {
		capped, short, seenCap := 0, 0, false
		for _, g := range gaps {
			if g >= capSec {
				capped++
				seenCap = true
			} else if seenCap && g < capSec/2 {
				short++
			}
		}
		out["cappedGaps"] = float64(capped)
		out["shortGapsAfterCap"] = float64(short)
	}
	// The stable attempt is the longest-lived exited one.
	stable, life := -1, 0.0
	for i, a := range att {
		if a.Exited.IsZero() {
			continue
		}
		if d := a.Exited.Sub(a.Launched).Seconds(); d > life {
			stable, life = i, d
		}
	}
	if stable >= 0 {
		out["stableSec"] = life
		grown := 0.0
		for i := 1; i <= stable; i++ {
			grown = math.Max(grown, gaps[i-1])
		}
		if stable > 0 {
			out["grownGapSec"] = grown
		}
		// The first replacement follows detection; the gap after it shows
		// whether the delay restarted from the beginning, and the next one
		// that it grows again from there.
		if stable+2 < len(att) {
			out["postResetGapSec"] = gaps[stable+1]
		}
		if stable+3 < len(att) {
			out["postResetGrowth"] = boolMetric(gaps[stable+2] > gaps[stable+1])
		}
	}
	if w := ev.Negative; w != nil && w.Until.After(w.Since) {
		out["negativeSec"] = w.Until.Sub(w.Since).Seconds()
		// The window is closed: a launch at either end counts.
		for _, a := range att {
			if !a.Launched.Before(w.Since) && !a.Launched.After(w.Until) {
				out["negativeSec"] = 0
			}
		}
	}
	if r := ev.Replacement; r != nil {
		ordered := len(r.Old) > 0 && r.New.PID != 0 && r.New.Created != 0
		for _, p := range r.Old {
			if p.Exited == 0 || p.Exited >= r.New.Created || p.PID == r.New.PID && p.Created == r.New.Created {
				ordered = false
			}
		}
		out["orderedReplacement"] = boolMetric(ordered)
	}
	if ev.Kept != nil {
		out["kept"] = boolMetric(*ev.Kept)
	}
	for k, v := range ev.Values {
		out[k] = v
	}
	if ev.Peer != nil {
		out["peerUnchanged"] = boolMetric(*ev.Peer)
	}
	if ev.Drained != nil {
		out["drained"] = boolMetric(*ev.Drained)
	}
	if status != nil {
		out["startLimited"] = boolMetric(status.Reason == "start-limit" && status.ActiveState == "failed")
		out["restartAttempts"] = float64(status.RestartAttempt)
		if b := status.Budget; b != nil {
			out["unlimited"] = boolMetric(b.Burst == 0 && b.Remaining == nil)
			if b.Burst > 0 && len(att) > 0 {
				out["withinBurst"] = boolMetric(len(att) <= b.Burst)
			}
		}
	}
	return out
}

func boolMetric(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// maxInWindow is the largest number of launches within any window of width w.
func maxInWindow(att []Attempt, w time.Duration) int {
	best, j := 0, 0
	for i := range att {
		for att[i].Launched.Sub(att[j].Launched) >= w {
			j++
		}
		best = max(best, i-j+1)
	}
	return best
}

// meets checks every requirement against the computed metrics.
func meets(reqs []Requirement, got map[string]float64) []string {
	var failed []string
	for _, r := range reqs {
		v, ok := got[r.Metric]
		switch {
		case !ok:
			failed = append(failed, r.Metric+" not supported by the evidence")
		case r.Min != nil && v < *r.Min:
			failed = append(failed, r.Metric+" below its minimum")
		case r.Max != nil && v > *r.Max:
			failed = append(failed, r.Metric+" above its maximum")
		}
	}
	return failed
}
