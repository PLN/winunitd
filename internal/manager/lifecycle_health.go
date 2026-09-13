package manager

// Only a current, uncanceled probe owner may publish health. Probe I/O and
// subsequent watchdog cleanup remain outside the lifecycle decision lock.
func (m *Manager) acceptProbeHealth(owner runtimeIdentity, success bool, threshold int) (accepted, unhealthy bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := m.units[owner.name]
	if m.closed || !owner.currentLocked(m) || rt.stopping || rt.watchdog == nil || rt.proc == nil || rt.cleanupPending() {
		return false, false
	}
	if success {
		rt.probeFailures = 0
		rt.health = "ready"
		return true, false
	}
	threshold = max(1, min(threshold, 1000))
	rt.probeFailures = min(rt.probeFailures+1, threshold)
	unhealthy = rt.probeFailures >= threshold
	rt.health = "degraded"
	if unhealthy {
		rt.health = "unhealthy"
	}
	return true, unhealthy
}
