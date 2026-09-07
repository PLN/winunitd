# Post-beta test hardening

September 7, 2026. Follow-up to [issue #40](https://github.com/PLN/winunitd/issues/40).
These changes strengthen regressions without changing production behavior or
expanding the supported beta feature set.

## Added coverage

| Test | Behavior established |
| --- | --- |
| `TestPersistentMonthEndRecoveryAcrossEngineRestart` | Fire July 31, stop the engine, reopen its store with a new engine on September 1, coalesce the missed August 31 into one catch-up, and schedule October 31. A second reopen retains that catch-up; the next activation schedules December 31, skipping November's missing 31st. Callback joins make activation counts deterministic. |
| `TestProcessLimitEventAndCleanup` | Admit one real process to a Windows job with ProcessLimit=1, reject another assignment, receive the actual completion-port notification, retain ResourceLimitHit after close, and confirm process cleanup. The rejected process remains separately owned by the fixture. |
| `TestLimitLoopDeliveryAndPortClose` | Exercise all four recognized limit message kinds through a real completion port, repeated notifications, the public hit/channel observations, and bounded loop termination after port close. These injected messages complement the real process-limit event. |

The timer store holds no open file handles. Engine Stop joins callbacks before
the old store is discarded and OpenStore constructs the next instance. This
tests successful persistence across manager restarts, not atomic crash durability,
corrupt-state handling or failed writes. Those remain [R5.1](https://github.com/PLN/winunitd/issues/99).
Native memory-pressure qualification also remains distinct from injected message
coverage and existing resource configuration/manager recovery tests.

## Timing assertion audit

- Portable path, registry and event-log tests now acknowledge the next watcher
  receive after the synchronous activation callback returns. Running-service,
  PathExists AND, repeated-satisfaction and deletion assertions therefore inspect
  processed events instead of assuming 200 ms was sufficient.
- Unrelated fake events and disabled-watch boot cases use their synchronous
  dispatch/startup boundary; no event was queued, so a sleep adds no evidence.
- The user-host listener test closes its input and joins Listen before asserting
  logoff cleanup. Logon retains a bounded positive condition wait. Recovery
  identity tests already use delayed workers, gate synchronization and bounded
  completion waits; no sleep-based negative assertion remains in those tests.
- Restart cancellation tests already use held exits and fake-clock advancement.
  The remaining sleeps in restart_test.go are the bounded positive polling helper
  and the legacy scripted launcher's delayed exit stimulus, not negative assertion
  windows. Their existing coverage is retained.
- Native path/registry/event-log delivery and real helper watchdog tests retain
  bounded wall-clock observation windows. They exercise OS delivery and actual
  helper heartbeats, which the fake manager clock does not control. These are
  smoke observations, not deterministic proof of absence for arbitrary time;
  portable synchronized tests provide the corresponding policy assertions.

## Verification

Local Windows amd64, Go 1.27.1:

- The new calendar/runtime tests and modified PathExists/user-host tests passed
  20 race-enabled repetitions. Changed path/event-log/registry and disabled-watch
  cases also passed 10 focused race-enabled repetitions.
- `go test -race -parallel 1 ./... -timeout 180s` and `go vet ./...` passed.
- Cross-platform CI results for the final revision belong in the linked PR/issue,
  with exact revision identity. Local Windows results do not stand in for Linux.

SYSTEM-only and other identity-gated tests retain explicit skip behavior. This
work does not claim new SYSTEM/session, VM, MSI or pilot qualification and does
not close R0-R8 acceptance gates. Raw local test output stays outside the repository.
