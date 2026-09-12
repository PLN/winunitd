package manager

import "slices"

// The reserved reconciliation pass owns this bounded completion channel and
// joins every admitted request. Reserve native capacity before spawning; a slow
// earlier query does not delay later sessions when another slot is available.
func (h *UserHost) dispatchReconcileRequests(requests map[uint32]uint64) {
	ids := make([]uint32, 0, len(requests))
	for id := range requests {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	h.mu.Lock()
	cursor := h.lastReconcileSession
	h.mu.Unlock()
	start := 0
	for start < len(ids) && ids[start] <= cursor {
		start++
	}
	ids = append(append(make([]uint32, 0, len(ids)), ids[start:]...), ids[:start]...)
	done := make(chan struct{}, maxNativeUserWork)
	next, active := 0, 0
	for next < len(ids) || active > 0 {
		for next < len(ids) && active < maxNativeUserWork {
			work, err := h.acceptNativeUserWork()
			if err != nil {
				break
			}
			id := ids[next]
			next++
			active++
			h.mu.Lock()
			h.lastReconcileSession = id
			h.mu.Unlock()
			go func() {
				h.queryAcceptedUserLogon(id, requests[id], work)
				done <- struct{}{}
			}()
		}
		if active == 0 {
			break
		}
		<-done
		active--
	}
	for _, id := range ids[next:] {
		h.finishSessionRequest(id, requests[id])
	}
}
