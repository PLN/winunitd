# Post-beta tracking

September 7, 2026. The public 0.2.1-beta delivery gates B1-B4 are complete.
Prioritize adopter feedback and reproducible defects in the supported core, then
continue the architecture and qualification backlog. Preserve documented beta
syntax. No new release or milestone closure is implied by this tracking update.

## Milestones

GitHub milestones mirror [repository acceptance gates](MILESTONES.md). R0-R2
are in progress; R3-R8 remain planned, with some groundwork already delivered.
Planned milestones may have no implementation issues yet; their work packages
remain in MILESTONES.md. There are no promised due dates.

| Gate | GitHub tracking |
| --- | --- |
| R0 | [Reproducible baseline and qualification harness](https://github.com/PLN/winunitd/milestone/1) |
| R1 | [Ownership, output, and failure containment](https://github.com/PLN/winunitd/milestone/2) |
| R2 | [Authoritative lifecycle coordinator](https://github.com/PLN/winunitd/milestone/3) |
| R3 | [Unit semantics, compatibility, and health](https://github.com/PLN/winunitd/milestone/4) |
| R4 | [Windows identities and manager qualification](https://github.com/PLN/winunitd/milestone/5) |
| R5 | [Durable timers and operational diagnostics](https://github.com/PLN/winunitd/milestone/6) |
| R6 | [Fully qualified serviceable MSI](https://github.com/PLN/winunitd/milestone/7) |
| R7 | [Pilot replacement and soak](https://github.com/PLN/winunitd/milestone/8) |
| R8 | [Verified signed installation release](https://github.com/PLN/winunitd/milestone/9) |

## Focused follow-up

[Test hardening #40](https://github.com/PLN/winunitd/issues/40) belongs to R0.
The [test audit](TEST-HARDENING.md) records coverage and evidence boundaries.
Its completion does not qualify experimental timers/resource controls for beta.
The following issues make the next work visible without replacing the full gates.

| Gate | Work |
| --- | --- |
| R0 | [R0.4: Complete repeatable Windows qualification harness](https://github.com/PLN/winunitd/issues/93) |
| R1 | [R1.1-R1.3: Finish ownership and cleanup qualification evidence](https://github.com/PLN/winunitd/issues/94) |
| R1 | [R1.4: Qualify journal fairness under aggregate overload](https://github.com/PLN/winunitd/issues/95) |
| R2 | [R2.1-R2.2: Migrate lifecycle decisions to one coordinator](https://github.com/PLN/winunitd/issues/96) |
| R2 | [R2.3: Bound admission and reserve lifecycle completion capacity](https://github.com/PLN/winunitd/issues/97) |
| R2 | [R2.4: Give accepted operations internal deadlines and cancellation ownership](https://github.com/PLN/winunitd/issues/98) |
| R5 | [R5.1: Make persistent timer state atomic and failures observable](https://github.com/PLN/winunitd/issues/99) |

The R2.4 operation-lifetime slice is implemented; see [behavior and limits](OPERATIONS.md).
Repeatable oneshots are the prioritized semantic slice (below); remaining
R2.1-R2.2 state-writer/coordinator work continues in #96. Graceful stop, semantic migration,
SYSTEM/user qualification, full MSI servicing and signing keep their separate
gates. Successful beta installation and maintenance tests do not close them.

## Priority: repeatable oneshots

September 12, 2026: prioritize the oneshot portion of R3.3 as the next feature
slice, independently of ExecStop. Default to `RemainAfterExit=no` so successful
oneshots finish inactive and a subsequent start executes a fresh invocation;
`RemainAfterExit=yes` retains the completed active state. No new `Type=task` is
needed. The concrete use case is a maintenance unit outside its workload target
that stops/updates/restarts those workloads while its own manager stays running.

Acceptance: repeated starts after completion execute again; compatible starts during an
existing invocation do not overlap or silently queue another run; failure,
timeout, cancellation, output capture and process cleanup remain truthful;
dependent ordered work proceeds after successful completion despite the oneshot
becoming inactive; explicit retained-active behavior still works. Cover these
through public lifecycle paths, including reload and stop/restart interleavings.
The accepted default changes directly: omitted `RemainAfterExit` means `no`.
No installed-base compatibility layer, format switch or converter is required
for this change. `RemainAfterExit=yes` explicitly requests retained-active behavior.

Local implementation and focused tests are present; merge/CI acceptance is still
pending. This does not close R2/R3 or migrate the installed pilot.
Elevation, external updater ownership, durable request/result
handling and removal of legacy task dependencies remain separate maintenance
integration work. A runner hosted by the daemon cannot update that daemon while
it is stopped.

Local Windows validation (September 12): `go test -race -parallel 1 ./...
-count=1 -timeout 180s` and `go vet ./...` passed; full command 54.1 seconds,
manager package 44.5 seconds. Focused oneshot/parser/native-repeat checks passed
20 race-enabled repetitions; the final recovery-cleanup regression and related
oneshot/cleanup tests passed ten repetitions before the final full suite.
[Parser tests](../internal/unit/oneshot_test.go) and
[lifecycle tests](../internal/manager/oneshot_test.go) cover defaults, retained
state, repeat execution, shared prerequisites, failure, cancellation, timeout,
restart, reload and cleanup failure. Existing native output/process-tree and
timer tests also pass. Hosted CI, merge and deployment remain separate.

## Dependency and CI follow-up

September 9, 2026: [go-winio PR #369](https://github.com/microsoft/go-winio/pull/369)
is still open and unmerged. Retain the documented local v0.6.2 patch and its
100-repeat Windows regression. Recheck by October 9, 2026, or sooner on an
upstream merge/release notification; replacement requires equivalent close-race
coverage and dependency/source-hash review. A clean vulnerability scan alone does
not audit a local module replacement.

Keep hosted CI on deliberate dispatch for now: the recorded workflow cost is
roughly 9-10 runner-minutes per full run, so nightly runs would add about 270-300
minutes per month. Require a successful exact-head dispatch before merging code;
record its run ID in the PR and handoff. Reconsider scheduled CI if the cost
budget changes or unverified merges recur. This is a reviewed cost decision,
not a claim that pushing automatically tests main.
