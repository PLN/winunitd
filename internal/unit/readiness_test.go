package unit

import (
	"strings"
	"testing"
	"time"
)

func TestReadinessProbeFormatAndBounds(t *testing.T) {
	const base = "[Unit]\nFormatVersion=2\n[Service]\nExecStart=C:\\Apps\\worker.exe\n"
	for _, policy := range []string{
		"ReadinessMode=http\nReadinessEndpoint=http://127.0.0.1:8080/ready\n",
		"ReadinessMode=tcp\nReadinessEndpoint=localhost:8080\n",
	} {
		r := ParseUnit("work.service", base+policy)
		if r.HasError() {
			t.Fatal(r.Issues)
		}
		s := r.Unit.Service
		if !s.HasReadinessProbe() || !s.WaitsForReadiness() || s.ReadinessIntervalSec != 100*time.Millisecond || s.ReadinessTimeoutSec != time.Second || s.TimeoutStartSec != 90*time.Second || !s.TimeoutStartSecSet {
			t.Fatal("readiness defaults lost")
		}
		if r := ParseUnit("work.service", strings.Replace(base, "FormatVersion=2", "FormatVersion=1", 1)+policy); !r.HasError() {
			t.Fatal("legacy format accepted readiness")
		}
		for _, typ := range []string{"notify", "oneshot", "scm", "scheduled-task"} {
			if r := ParseUnit("work.service", base+"Type="+typ+"\n"+policy); !r.HasError() {
				t.Fatal("unsupported readiness service type", typ)
			}
		}
	}
	for _, policy := range []string{
		"ReadinessMode=http", "ReadinessMode=none", "ReadinessMode=", "ReadinessEndpoint=http://127.0.0.1:8080/",
		"ReadinessMode=http\nReadinessEndpoint=http://example.com/", "ReadinessMode=http\nReadinessEndpoint=http://alice:secret@127.0.0.1/", "ReadinessMode=tcp\nReadinessEndpoint=192.0.2.1:8080",
		"ReadinessMode=tcp\nReadinessEndpoint=127.0.0.1:8080\nReadinessExpectedStatus=200",
	} {
		if r := ParseUnit("work.service", base+policy+"\n"); !r.HasError() {
			t.Fatal("accepted invalid readiness policy", policy)
		}
	}
	valid := "ReadinessMode=http\nReadinessEndpoint=http://127.0.0.1:8080/ready\n"
	for _, policy := range []string{"ReadinessIntervalSec=0", "ReadinessIntervalSec=infinity", "ReadinessTimeoutSec=0", "ReadinessTimeoutSec=infinity", "TimeoutStartSec=0", "TimeoutStartSec=infinity", "ReadinessExpectedStatus=600"} {
		if r := ParseUnit("work.service", base+valid+policy+"\n"); !r.HasError() {
			t.Fatal("accepted unbounded readiness", policy)
		}
	}
}
