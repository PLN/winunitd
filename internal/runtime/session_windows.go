//go:build windows

package runtime

import (
	"context"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// WatchSessionsInterval is how often console mode re-enumerates sessions.
const WatchSessionsInterval = 2 * time.Second

// InteractiveSessions returns session IDs that are logged on (active,
// connected, or disconnected). Session 0 and listen sockets are skipped.
func InteractiveSessions() ([]uint32, error) {
	var info *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &info, &count); err != nil {
		return nil, err
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(info)))
	if count == 0 || info == nil {
		return nil, nil
	}
	sessions := unsafe.Slice(info, int(count))
	var ids []uint32
	for _, s := range sessions {
		if s.SessionID == 0 {
			continue
		}
		switch s.State {
		case windows.WTSActive, windows.WTSConnected, windows.WTSDisconnected:
			ids = append(ids, s.SessionID)
		}
	}
	return ids, nil
}

// ParseSessionChange maps an SCM SESSIONCHANGE request to a SessionChange.
// Disconnect/lock are not logoff. Unknown event types are ignored.
// eventData is the SCM EVENTDATA pointer, converted at the handler
// boundary — not carried as a uintptr and converted later (checkptr).
func ParseSessionChange(cmd svc.Cmd, eventType uint32, eventData unsafe.Pointer) (SessionChange, bool) {
	if cmd != svc.SessionChange {
		return SessionChange{}, false
	}
	id := sessionIDFromEvent(eventData)
	switch eventType {
	case windows.WTS_SESSION_LOGON, windows.WTS_CONSOLE_CONNECT, windows.WTS_REMOTE_CONNECT:
		return SessionChange{SessionID: id, Logon: true}, true
	case windows.WTS_SESSION_LOGOFF, windows.WTS_SESSION_TERMINATE:
		return SessionChange{SessionID: id, Logon: false}, true
	default:
		return SessionChange{}, false
	}
}

func sessionIDFromEvent(eventData unsafe.Pointer) uint32 {
	if eventData == nil {
		return 0
	}
	n := (*windows.WTSSESSION_NOTIFICATION)(eventData)
	return n.SessionID
}

// WatchSessions enumerates interactive sessions and emits logon/logoff
// diffs until ctx is done. Used for console mode and as a backup to SCM
// session-change notifications.
func WatchSessions(ctx context.Context, out chan<- SessionChange) {
	if out == nil {
		return
	}
	prev := map[uint32]struct{}{}
	tick := func() {
		ids, err := InteractiveSessions()
		if err != nil {
			return
		}
		now := make(map[uint32]struct{}, len(ids))
		for _, id := range ids {
			now[id] = struct{}{}
			if _, ok := prev[id]; !ok {
				select {
				case out <- SessionChange{SessionID: id, Logon: true}:
				case <-ctx.Done():
					return
				}
			}
		}
		for id := range prev {
			if _, ok := now[id]; ok {
				continue
			}
			select {
			case out <- SessionChange{SessionID: id, Logon: false}:
			case <-ctx.Done():
				return
			}
		}
		prev = now
	}
	tick()
	ticker := time.NewTicker(WatchSessionsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}
