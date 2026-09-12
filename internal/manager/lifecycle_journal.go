package manager

import (
	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/runtime"
)

// The unit gate excludes another attachment. Launch ownership retains the record
// even when stop supersedes this launch before the process is returned.
func (m *Manager) attachMainCapture(effect *launchEffect, proc runtime.Process, invocation string) {
	m.journal.SetOrigin(m.journalOrigin())
	capture := m.journal.Attach(effect.owner.name, proc.PID(), invocation, proc.Stdout(), proc.Stderr())
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.units[effect.owner.name] == effect.owner.record {
		effect.owner.record.capture = capture
	}
}

// Journal completion belongs to its exact capture, independently of process
// generation. A late stop wait cannot clear a replacement invocation's capture.
func (m *Manager) publishJournalCompletion(name string, record *unitRuntime, capture *journal.Capture, generation uint64, complete bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if record == nil || m.units[name] != record || record.capture != capture {
		return
	}
	if capture == nil && record.gen != generation {
		return
	}
	record.setCleanup(cleanupJournal, !complete)
	if complete {
		record.capture = nil
	}
}
