# Post-beta tracking

September 7, 2026. The public 0.2.1-beta delivery gates B1-B4 are complete.
Prioritize adopter feedback and reproducible defects in the supported core, then
continue the architecture and qualification backlog. Preserve documented beta
syntax. No new release or milestone closure is implied by this tracking update.

## Milestones

GitHub milestones mirror [repository acceptance gates](MILESTONES.md). R0-R6
are in progress; R7-R8 remain planned, with the packaging spike already delivered.
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
Repeatable oneshots are merged (below). As of September 13, tracked ExecStop,
user-session reconciliation/recovery, SCM readiness and global maintenance are
also implemented. Native SYSTEM/interactive and headless-user results have
expanded; see [R2 evidence](R2-EVIDENCE.md), [stop evidence](R3-EVIDENCE.md) and
the [maintenance contract](OPERATIONS.md#global-maintenance). Remaining
R2.1-R2.2 state-writer/coordinator work continues in #96. The full health/dependency contract,
session/security matrix, MSI servicing and signing retain their
separate gates. The live pilot handoff and seven-day soak have not begun;
successful beta installation and selected runtime tests do not close them.

September 13: Fable issues #162-#165 are closed by PR #166-#169: unrelated
dependency allocation, journal retention, staticcheck enforcement and snapshot
encoding are fixed. PR #170 adds qualified native proxy disappearance handling;
PR #171-#172 retain output/bootstrap cleanup and join listener/job workers.
The exact-source SYSTEM/headless-user results are recorded in
[ownership evidence](R1-EVIDENCE.md#september-13-native-close-and-bootstrap-qualification)
and [proxy evidence](R3-EVIDENCE.md#native-proxy-inactivity-observation).
These merges advance R1/R3/R5 without closing the remaining acceptance gates.

September 13 follow-up: format-2 reference/migration is delivered in PR #175;
startup/recovery policy and selected MSI paths in PR #177; snapshot failure
diagnostics in PR #178; consolidated R1 ownership/cleanup qualification in
PR #179. [R1 acceptance evidence](R1-EVIDENCE.md#consolidated-ownership-and-cleanup-acceptance)
completes the technical scope of #94. [Installer evidence](R6-EVIDENCE.md) moves
R6 into progress. Remaining milestone exit gates and the pilot soak stay open.

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

Repeatable oneshots and unit-hosted maintenance merged in
[PR #116](https://github.com/PLN/winunitd/pull/116) after
[exact-head Windows/Linux CI](https://github.com/PLN/winunitd/actions/runs/34680935557)
passed at `c1a87e3`. This does not close R2/R3 or qualify SCM pilot migration.
External updater ownership, durable request/result
handling and removal of legacy task dependencies are covered by the separate maintenance
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

The unit-hosted Hermes maintenance helper is implemented separately in
[tools/maintenance](../tools/maintenance/README.md). It keeps the user manager
running, uses a disabled target as a persistent boot hold, removes the routine
need for elevated task manipulation, and retains durable request/results and
pre-/post-mutation recovery rules. It does not implement the R4.3 global barrier
or migrate interactive startup to SCM. Local native read-only rehearsal and an
injected pre-mutation failure/recovery succeeded; actual application-version
updates and reboot recovery still need separate qualification.

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
