package nestedjob

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// CommandServer reads one role's commands from Dir strictly in sequence and
// writes an acknowledgment for each, including rejected ones. Only
// ValidateCommand's enumerated verbs reach Handle.
type CommandServer struct {
	Dir    string
	Role   string
	Handle func(Command) Ack
	next   int
}

// Poll handles the next command when it is present and reports whether it
// did. An absent or transiently unreadable file is retried later.
func (s *CommandServer) Poll() (bool, error) {
	if s.next == 0 {
		s.next = 1
	}
	var cmd Command
	err := ReadJSON(filepath.Join(s.Dir, CommandFile(s.Role, s.next)), &cmd)
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return false, nil
	}
	ack := Ack{Seq: s.next, Verb: cmd.Verb}
	switch {
	case err != nil:
		ack.Failure = &Failure{Op: "command", Message: err.Error()}
	case cmd.Seq != s.next:
		ack.Failure = &Failure{Op: "command", Message: fmt.Sprintf("sequence %d in file %d", cmd.Seq, s.next)}
	default:
		if verr := ValidateCommand(s.Role, cmd); verr != nil {
			ack.Failure = &Failure{Op: "command", Message: verr.Error()}
		} else {
			ack = s.Handle(cmd)
		}
	}
	s.next++
	return true, WriteJSON(filepath.Join(s.Dir, AckFile(s.Role, ack.Seq)), ack)
}
