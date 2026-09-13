# Durable timers and diagnostics qualification

## Journal rotation failure and retry

PR #192 source `7c114fb04d653dc5ae8bc254dc71359a136ebd48` passed
[exact-source CI](https://github.com/PLN/winunitd/actions/runs/34761951461).
The previous implementation silently succeeded when a middle archive was locked
on Windows. Rotation now checks every file operation and retains the failed step
until repair, preventing repeated retries from evicting additional generations.

Disposable LTSC build 26100 qualification ran the following five cases ten times
each as SYSTEM and a headless standard user, with no skips:

- Real Windows archive sharing violation, three rejected retries, then exact
  retained-history recovery after handle release.
- Six injected close/delete/archive-shift/current-rename/replacement-open phases,
  with repeated capture cleanup retaining ownership, visible loss/storage errors,
  and successful repair without duplicate or missing retained records.
- Normal per-unit generation/size retention and chronological reading.
- Retirement followed by interrupted-tail repair, reopening and rotation.
- Failed write/close retirement retaining ownership until recovery.

The suites took 5.122 seconds as SYSTEM and 5.183 seconds as the headless user.
Final process/profile/linger cleanup passed; the hosting broker stayed running.
Exact artifacts, module hashes, CI, full logs and cleanup observations are retained
privately. Merge `fb68cb833aa777a5bf09e9d466f2f9e8f188c39b` has the tested tree.
Twenty focused local race repetitions, the full uncached race suite, vet and
Windows/Linux staticcheck passed; three combined proxy/rotation repetitions passed
after integration.

This qualifies retryable rotation errors. Progress is in-memory; multi-file
rotation is not an atomic crash/power-loss transaction. Earlier file-handle and
per-name-counter bounds remain implemented. Total historical disk retention,
durable daemon diagnostics and the complete R5 acceptance remain open.

## Timer state replacement and interrupted activation

PR #185 source `1d6719dc856499b8280d4570248ba634ecea6056` passed
[exact-source CI](https://github.com/PLN/winunitd/actions/runs/34759062624).
Merge `b4a09bb0b4ac8fb71dfbdd0afb332474b1f270b5` has the same tested tree.
The [storage contract](OPERATIONS.md#timer-storage-work) combines checked bounded
reads, versioned state, pending/result activation records and dispatch suspension
with explicit platform replacement durability requests. Windows uses same-directory
write-through replacement; Unix also syncs the parent directory after rename.

The disposable Windows 11 Enterprise LTSC build 26100 matrix ran every case below
ten times as SYSTEM and ten times as a headless standard user. No selected test
skipped. The combined suites took 5.409 seconds and 4.501 seconds respectively.

| Required case | Behavior qualified |
| --- | --- |
| `TestTimerStoreFileFailuresSuspendDispatch` | Create, partial/short write, flush, close, replacement and acknowledgement failures suspend the real engine; repair recovers old or complete pending state without changing its identity |
| `TestTimerStoreProcessCrashAtReplacement` | Separate process exits without deferred cleanup before/after replacement; restart reads the complete old/new record and ignores orphan temporary files |
| `TestWindowsTimerStoreLockedReplacement` | Real Windows sharing violation preserves the old record; replacement succeeds after handle release |
| `TestTimerStoreRejectsCorruptionAndPreservesReplacement` | Truncated/null/oversized state, unsupported versions and invalid timestamps fail visibly |
| `TestPendingTimerIntentRecoversOnceAndCoalescesMissedCalendar` | Pending ID survives recovery; missed occurrences coalesce; completed result is not replayed on another restart |
| `TestTimerFailedResultWriteRetainsRetryableIntent` | Result failure retains the durable pending record and same retry identity |
| `TestTimerIntentStateMigrationAndValidation` | Legacy/version-1 timestamps upgrade on save; invalid intents fail visibly |
| `TestPendingTimerIntentRejectsRetargeting` | Changed persistent target suspends dispatch |
| `TestTimerWriteFailureSuspendsActivationAndCanBeRepaired` | Required durable-write failure prevents dispatch and repair resumes it |
| `TestTimerStaleBlockedWriteCannotActivateReplacement` | Late persistence cannot dispatch a replacement arm |
| `TestPersistentMonthEndRecoveryAcrossEngineRestart` | Month-end recovery survives engine replacement |

Final teardown removed the fixture, disabled linger, released user manager/helper
processes and unloaded the profile. The hosting broker stayed running. Binary and
module hashes, CI identity, raw logs, exact pass counts and cleanup observations
are retained privately. Ten focused local race repetitions, the full uncached
race suite, vet and Windows/Linux staticcheck passed; combined timer/readiness
regressions passed after integration with the readiness changes.

Earlier real-daemon qualification at `e0a9ab7` also recovered a pending timer after
a broker crash in 2.689 seconds, preserving activation identity and avoiding a
second replay; [recorded evidence](R3-EVIDENCE.md#managed-bound-dependent-cleanup).
The delivery policy permits duplicate work when execution precedes durable result
publication, so persistent workloads must be idempotent.

This delivers the R5.1 technical slice and issue #99's crash/failure-injection
scope. The evidence covers process crashes and the tested local filesystem; it
does not establish universal power-loss durability for controllers or remote
filesystems. Complete clock/DST/resume, journaling retention, diagnostics, stress,
installer state rollback and overall R2/R3/R5 acceptance remain separate gates.
