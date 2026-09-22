package winevt

import "github.com/PLN/winunitd/internal/journal"

// reportEvent is the platform reporter. Tests replace it; production uses ReportEvent.
var reportEvent = report

// Emit reports one accepted daemon-log record. It runs on the daemon-log
// writer, ignores reporting errors, and must not record another daemon event.
func Emit(view journal.DaemonEventView) {
	id, kind, insert := Format(view)
	reportEvent(id, kind, insert)
}

// EmitStartupFailure reports a startup failure when the daemon log cannot be
// opened. The same redaction rules apply. Reporting errors are ignored.
func EmitStartupFailure(reason string) {
	cleaned, _ := journal.PublicDetail(reason)
	reportEvent(IDStartupFailed, KindError, cleaned)
}
