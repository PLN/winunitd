package unit

import (
	"testing"
	"time"
)

func TestWatchdogProbePolicyValidation(t *testing.T) {
	base := "[Unit]\nFormatVersion=2\n[Service]\nExecStart=C:\\Apps\\worker.exe\nWatchdogMode=http\nWatchdogEndpoint=http://127.0.0.1:8080/health\nWatchdogSec=1s\n"
	for _, policy := range []string{"WatchdogGraceSec=infinity", "WatchdogGraceSec=-1", "WatchdogGraceSec=", "WatchdogTimeoutSec=0", "WatchdogTimeoutSec=infinity", "WatchdogTimeoutSec=", "WatchdogFailureThreshold=0", "WatchdogFailureThreshold=1001", "WatchdogFailureThreshold=1.5", "WatchdogFailureThreshold="} {
		if result := ParseUnit("work.service", base+policy+"\n"); !result.HasError() {
			t.Fatal("accepted invalid probe policy", policy)
		}
	}
	for _, prefix := range []string{
		"[Service]\nExecStart=C:\\Apps\\worker.exe\nWatchdogMode=tcp\nWatchdogEndpoint=127.0.0.1:8080\nWatchdogSec=1s\n",
		"[Unit]\nFormatVersion=2\n[Service]\nExecStart=C:\\Apps\\worker.exe\nWatchdogMode=notify\nWatchdogSec=1s\n",
		"[Unit]\nFormatVersion=2\n[Service]\nType=scm\nServiceName=example\n",
	} {
		if result := ParseUnit("work.service", prefix+"WatchdogFailureThreshold=2\n"); !result.HasError() {
			t.Fatal("accepted unsupported probe mode/format")
		}
	}
	result := ParseUnit("work.service", base+"WatchdogGraceSec=3s\nWatchdogTimeoutSec=200ms\nWatchdogFailureThreshold=3\n")
	if result.HasError() {
		t.Fatal(result.Issues)
	}
	spec := result.Unit.Service
	if spec.WatchdogGraceSec != 3*time.Second || spec.WatchdogTimeoutSec != 200*time.Millisecond || spec.WatchdogFailureThreshold != 3 {
		t.Fatal("parsed policy lost values")
	}
}
