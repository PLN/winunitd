package journal

import (
	"strings"
	"time"
	"unicode/utf8"
)

const maxDiagnosticMessage = 4096

// RecordDiagnostic queues a daemon decision without filesystem or console I/O.
// Callers supply the accepted unit/invocation identity. One shared queue group
// has the same byte/record allowance and loss accounting as workload capture;
// Store.Close joins accepted records. Messages are bounded UTF-8 copies.
func (s *Store) RecordDiagnostic(unit, invocation, message string) {
	if s == nil {
		return
	}
	partial := len(message) > maxDiagnosticMessage
	if partial {
		message = message[:maxDiagnosticMessage]
	}
	message = strings.ToValidUTF8(message, "\uFFFD")
	if len(message) > maxDiagnosticMessage {
		partial = true
		message = message[:maxDiagnosticMessage]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	origin := s.snapshotOrigin()
	s.enqueue(Entry{
		Timestamp: time.Now().UTC(), Unit: strings.Clone(canonicalUnit(unit)),
		InvocationID: strings.Clone(invocation), Message: strings.Clone(message),
		Stream: "daemon", Severity: SeverityErr, Partial: partial,
		Session: origin.Session, UserSID: origin.UserSID,
	}, &s.diagnostics)
}
