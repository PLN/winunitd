# Post-beta test hardening

## Constrained Windows acceptance

September 13, 2026: the technical scope of
[issue #112](https://github.com/PLN/winunitd/issues/112) is complete. PR #212 source
`7396beefb88b4400ff10842bdc86f5cfd433cb38` passed
[exact-source Windows/Linux CI](https://github.com/PLN/winunitd/actions/runs/34772550381);
merge `912d84197ed2211704d9a36f537758fc1bc03ee6` has the tested tree. R0.4's
clean-input provisioning/session harness and broader R4 acceptance remain open.

The runner and inherited test processes were constrained to one logical CPU,
with `GOMAXPROCS=1` and test parallelism one. The uncached source-tree full race
command passed in 176.962s, including manager 61.583s and journal 49.665s. This
fits the existing 180s full-command and 90s manager review budgets, with little
full-command margin. Cold hosted setup remains a separate measurement.

On disposable Windows Enterprise LTSC build 26100, the same four clean native
manager/journal/timer/notification binaries passed these constrained matrices:

| Identity | Selected cases | Repetitions per case | Suite time |
| --- | --- | --- | --- |
| SYSTEM | 105 | 3 | 53.871s |
| Headless standard user with temporary Application-log test access | 101 | 3 | 47.315s |

No selected case skipped. The matrix covers notify/watchdog policy and native
helpers, late-client ownership, real directory/registry/event delivery, and
existing storage/clock/notification regressions. SYSTEM covers HKLM operations;
the standard-user lane covers the loaded user hive. Native binaries are non-race;
the source-tree and hosted race suites are separate. CLI compilation/delivery
belongs to the source-tree lane, which has the Go toolchain. Both native runners'
CPU/Go constraints, source/module identity and all four binary hashes were checked.

The headless test account initially lacked Application-log read access. A reader
membership experiment permitted subscription, but these fixtures also publish
their own probe events and need write access for `RegisterEventSource`.
[Windows defines those access rights separately](https://learn.microsoft.com/en-us/windows/win32/eventlog/event-logging-security).
The final lane granted only that account Application-log read/write access for
the fixture. Exact original channel security and account membership were restored.
Subscription consumers need channel read access; the extra write right here is
for the fixture publisher. Neither earlier attempt's twelve skips counted as
qualification; both incomplete runs are retained separately.

Final teardown removed the fixture, disabled linger, unloaded the profile and
left no fixture, user-manager or desktop-helper process. The original broker
remained running. Full logs, exact scripts, permission backup/restoration,
manifests and failed attempts are retained privately. The pilot was unchanged.

Two controlled mutations check the assertion improvements: omitting the accepted
client join fails immediately; suppressing accepted watchdog health passes the
old scheduler-yield assertion and fails the revised positive-completion check.
A separate 100ms delayed-arm stimulus passes. Twenty focused race repetitions
passed in 2.607s, as did vet and Windows/Linux staticcheck. No production timeout,
native observation window or suite deadline was increased.

## September 13 scheduler-sensitive test inventory

The [accepted constrained-worker run](#constrained-windows-acceptance) covers
the following inventory. Policy checks use completed decisions or held
adapters; real transport and OS-event checks retain bounded observation windows.
The following inventory distinguishes those boundaries. A successful smoke
window does not prove absence for arbitrary time.

| Area | Assertion boundary and remaining wall-clock use |
| --- | --- |
| Notify start timeout, watchdog expiry/refresh, exact boundary, restart and redundant start (`internal/manager/notify_test.go`) | Fake-clock deadlines are armed before advancement; positive state/operation waits observe accepted completion. Transport retries poll at 20/50ms within existing bounded contexts. The repeated readiness sender spans stop/relaunch; it is stimulus, not a negative timing assertion. |
| TCP/HTTP watchdog policy (`internal/manager/watchdog_test.go`) | Fake time triggers probes; accepted public health replaces 100,000 scheduler yields. The combined notify/TCP fixture also waits for the consumed probe deadline to rearm because startup READY already sets health. Real loopback connect/HTTP requests retain their configured timeout. |
| Late notify-client cleanup (`internal/manager/notify_clients_test.go`) | A held Accept and `testing/synctest.Wait` establish that Close waits for its owned result. No 20ms scheduling window remains; failed late-client close still has to remain retryable. |
| Notification transport (`internal/notify`) | TCP acceptance has a 20ms pre-banner observation and a one-second connection deadline; payload and cancellation checks exercise real transport. Native pipe acceptance uses a five-second context. Admission uses held connections, explicit accept/close signals and five-second positive waits inside a 20-second context. |
| Native helper READY/watchdog (`internal/manager/manager_windows_test.go`) | Delayed READY uses a 300ms stimulus, two-second activating observation and five-second startup limit; missing READY has a 300ms startup limit. Heartbeat helpers send every 50ms against a 300ms watchdog with a 700ms active observation. Missing heartbeats use a 200ms watchdog and three-second failure observation. Real TCP/HTTP smoke retains 800ms observations with 300ms watchdogs. CLI compilation is part of the source-tree lane. |
| Manager path/registry/event policy (`internal/manager/{pathwatch,registry,eventlog}_test.go`) | Fake subscriptions acknowledge the next receive after synchronous activation; running-service, unrelated/disabled and AND/OR semantics use processed-event or startup boundaries. No elapsed negative window is needed. |
| Manager native path/registry/event delivery (`internal/manager/*_windows_test.go`) | Existing 800ms negative observations complement up-to-eight-second positive helper-count waits. They cover real directory/registry/event delivery and remain smoke checks alongside deterministic policy cases. |
| Native directory and registry adapters (`internal/pathwatch`, `internal/registry`) | Directory filter smoke retains 400ms observations followed by matching events with three-second delivery bounds. Registry set notification has a three-second bound. Protected-handle cleanup and late-open ownership use explicit adapter/worker synchronization. |
| Windows event subscription (`internal/eventlog/subscribe_windows_test.go`) | Actual Application event publication and subscription delivery use an eight-second bound. XML/filter parsing is synchronous policy coverage. |

Compiler/action identity remains pinned to Go 1.27.1. The accepted measurements
above complete #112; they do not close R0.4 or broader SYSTEM/session acceptance.

## Original September 7 checkpoint

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

## Suite runtime budget and remaining timing work

September 9, 2026: use `go test -race -parallel 1 ./... -timeout 180s` on Windows.
Record uncached full-command wall time and the manager package time beside the
source identity. The September 7 manager baseline was 45.1 seconds; compilation
and package scheduling make it different from total command wall time.
A provisional review budget is 90 seconds for the manager package and 180 seconds
for a warm-toolchain full command. Exceeding it triggers profiling and test
consolidation, not skipped assertions or larger timeouts. Hosted cold setup is
measured separately under the existing 15-minute job deadline.

The September 9 review follow-up measured 56.7 seconds for the uncached full
command (`-count=1`) and 45.1 seconds for internal/manager on the local Windows
worker; both remain within this provisional budget. The exact source revision
and cross-platform run belong in the associated PR.

Issue #40 follow-up: inventory remaining scheduler-sensitive notify/watchdog and
native-event windows, run them on a constrained Windows worker, and replace policy
waits with fake-clock/delayed-adapter handshakes. Keep real native-delivery smoke
windows explicitly identified. Prefer extending public Start/Stop/Reload tests
over adding a new test for every extracted handler; keep only table-level tests
needed to define transition or rejection contracts.
