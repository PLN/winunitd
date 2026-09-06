package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigurationRevisionAcceptanceAndRemoval(t *testing.T) {
	definition := "[Service]\nExecStart=C:\\Tools\\work.exe\n"
	m := managerWith(t, &fakeLauncher{}, map[string]string{"work.service": definition})
	other := managerWith(t, &fakeLauncher{}, map[string]string{"work.service": definition})
	initial, _ := m.Status("")
	second, _ := other.Status("")
	id := initial.Machine.ConfigRevision
	if id == "" || id == second.Machine.ConfigRevision {
		t.Fatal("independent managers reused a configuration identity")
	}
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	status, _ := m.Status("work")
	if status.Unit.ConfigRevision != id || status.Unit.InvocationConfigRevision != id {
		t.Fatal("first invocation did not capture the accepted revision")
	}
	writeUnit(t, m.cfg.UnitsDir(), "work.service", "[Service]\nType=invalid\n")
	rejected, err := m.Reload()
	if err != nil || len(rejected.Errors) == 0 || rejected.ConfigRevision != id {
		t.Fatal("rejected candidate changed acceptance identity")
	}
	status, _ = m.Status("work")
	if status.Unit.ConfigRevision != id || status.Unit.InvocationConfigRevision != id {
		t.Fatal("rejected candidate changed unit revision identities")
	}
	writeUnit(t, m.cfg.UnitsDir(), "work.service", definition)
	accepted, err := m.Reload()
	if err != nil || accepted.ConfigRevision == "" || accepted.ConfigRevision == id {
		t.Fatal("accepted reload did not publish a new revision")
	}
	status, _ = m.Status("work")
	if status.Unit.ConfigRevision != accepted.ConfigRevision || status.Unit.InvocationConfigRevision != id {
		t.Fatal("reload relabelled a live invocation")
	}
	if _, err := m.Enable("work"); err != nil {
		t.Fatal(err)
	}
	enabled, _ := m.Status("")
	if enabled.Machine.ConfigRevision == accepted.ConfigRevision {
		t.Fatal("enable graph swap did not receive a revision")
	}
	if _, err := m.Disable("work"); err != nil {
		t.Fatal(err)
	}
	disabled, _ := m.Status("")
	if disabled.Machine.ConfigRevision == enabled.Machine.ConfigRevision {
		t.Fatal("disable graph swap did not receive a revision")
	}
	if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), "work.service")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	status, err = m.Status("work")
	if err != nil || status.Unit.LoadState != "unavailable" || status.Unit.ConfigRevision != "" || status.Unit.InvocationConfigRevision != id {
		t.Fatal("removal lost captured revision or claimed a loaded definition")
	}
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	status, err = m.Status("work")
	if err != nil || status.Unit.InvocationConfigRevision != id {
		t.Fatal("stop lost the last invocation's configuration identity")
	}
}

func TestFailedStartPreparationDoesNotRelabelLastInvocation(t *testing.T) {
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		"work.service": "[Service]\nExecStart=C:\\Tools\\old.exe\n",
	})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	previous, _ := m.Status("work")
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, m.cfg.UnitsDir(), "work.service", "[Service]\nExecStart=C:\\Tools\\new.exe\n")
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	listener := &controlledNotifyListener{}
	listener.fail.Store(true)
	defer listener.fail.Store(false)
	m.mu.Lock()
	m.units["work.service"].notify = &notifyRuntime{lis: &notifyCloseListener{Listener: listener}, done: make(chan struct{})}
	m.mu.Unlock()
	if _, err := m.Start(context.Background(), "work"); err == nil {
		t.Fatal("injected preparation failure did not reject start")
	}
	status, _ := m.Status("work")
	if status.Unit.InvocationID != previous.Unit.InvocationID || status.Unit.InvocationConfigRevision != previous.Unit.InvocationConfigRevision {
		t.Fatal("failed preparation relabelled the previous invocation")
	}
	listener.fail.Store(false)
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	status, _ = m.Status("work")
	if status.Unit.InvocationID == previous.Unit.InvocationID || status.Unit.InvocationConfigRevision != status.Unit.ConfigRevision {
		t.Fatal("fresh invocation did not capture its accepted revision")
	}
}
