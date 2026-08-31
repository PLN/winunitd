// Package manager is the in-process unit manager used by winunitd.
//
// It loads unit files, tracks enablement and lifecycle state, and serves
// the control protocol. Start CreateProcess's ExecStart into a per-unit
// Job Object; stop kills that job. Restart= (no / always / on-failure)
// relaunches the main process into a new unit job. Enable/targets at
// boot and the journal store are later milestones.
package manager
