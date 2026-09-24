# Durable timers and diagnostics qualification

## Combined operational stress acceptance

R5.5 technical acceptance is complete as of September 14, 2026.
[PR #218](https://github.com/PLN/winunitd/pull/218), source
`3605c2c7978c4b10076558a76e5453fd051e60bc`, passed
[exact-source Windows/Linux CI](https://github.com/PLN/winunitd/actions/runs/34813927032).
Merge `136909debd9b9aa3d79b07fe18ab3d42b1225cbd` has the identical tested tree.
R5.4 diagnostics, rendered Application events, and overall R5 are accepted in
[diagnostics and event resources](#diagnostics-and-event-resources).

Windows 11 Enterprise LTSC 26100.9168 qualification ran 19 selected tests three
times per SYSTEM and headless standard-user identity, without skips. Each
identity completed nine injected-fault and nine actual disk-full combined
cycles, plus three standalone volume exhaustion/recovery cases. The suites took
55.701 and 53.236 seconds. One logical CPU and test parallelism one were enforced;
the outer runner used `GOMAXPROCS=1`, and each isolated combined manager process
used `GOMAXPROCS=2` on that same CPU.

The two combined cases, `TestManagerCombinedOperationalPressure` and
`TestManagerCombinedDisposableDiskPressure`, overlap five noisy native children
and one quiet child with a stalled writer, four retained slow journal scans,
128 excess reader requests, 128 native path writes and four exit-7 processes.
The noisy children emit 42.5 MiB across stdout/stderr, including unterminated
fragments. Path notifications may coalesce; the companion still reaches its
four-start limit and does not resurrect after disarming/stopping. Canceled
readers retain bounded scan admission until the underlying scans return.

The injected writer persists a 37-byte prefix and then returns disk-full errors.
The native variant fills a separately validated volume of 65,990,656 bytes. Both
retain cleanup ownership during failed persistence, account for dropped output,
recover the intact first 64 KiB fragment, and resume quiet logging and reads.
Storage is deliberately lossy under pressure; quiet output lost to actual disk
exhaustion must have loss/error counters and a successful new capture after repair.

| Measurement | Enforced fixture budget | SYSTEM maximum | Standard-user maximum |
| --- | --- | --- | --- |
| Manager status call | 1,000 ms | 11.691 ms | 27.419 ms |
| Six concurrent stops while storage stalls | 5,000 ms | 1,053.758 ms | 1,064.887 ms |
| Cleanup after restored storage | 20,000 ms | 205.930 ms | 254.639 ms |
| Queue bytes / records | 16 MiB / 16,384 | 14,639,191 / 16,384 | 14,639,191 / 16,384 |
| Heap increase over cycle baseline | 128 MiB | 39,151,992 bytes | 39,553,432 bytes |
| Private committed increase | 128 MiB native; 768 MiB with race instrumentation | 45,391,872 bytes | 44,142,592 bytes |
| Goroutine / handle / thread increase | 256 / 256 / 64 | 82 / 205 / 24 | 76 / 203 / 24 |

Each invocation retains at most 4 MiB of queued data. Sampled resident maxima
were 63,680,512 and 56,672,256 bytes, reported separately from heap/private commit.
All manager subprocesses returned to three goroutines; repeated cycles stayed
within 32 additional cached handles and four threads relative to the first
completed cycle. These are sampled measurements and assertions for this fixture,
not universal process-memory bounds. Status timings call the manager API directly;
the separate control/maintenance saturation evidence remains applicable.

The volume fixture truncates and flushes its owned filler, then verifies returned
capacity before timing recovery. Earlier attempts showed zero available bytes
for over 30 seconds after deletion alone; Windows may defer deletion until the
[last file handle closes](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-deletefilew).
Those failed attempts remain retained and are not acceptance evidence. No runtime
timeout was relaxed and no runtime code changed.

Final journal race tests passed locally in 64.469 seconds. Repeated local
aggregate/combined pressure tests and the full Windows race suite also passed;
final-source CI includes Windows/Linux race suites, vet, staticcheck,
vulnerability checks and builds. Four exact-source test binaries and module
hashes were verified. Fixture processes, profile and linger ownership were
released, the disposable VHD detached, and the original hosting broker stayed
running. Raw logs, measurements, artifact identities and cleanup records remain
private. The binaries embed the candidate manager; this does not qualify a new
broker installation, MSI servicing, physical power-loss durability or the pilot.

## Journaling acceptance

R5.3 technical acceptance is complete as of September 13, 2026. The R2/R3
dependencies are satisfied. R5.4 diagnostics and rendered Application events
are accepted in [diagnostics and event resources](#diagnostics-and-event-resources);
combined operational stress is accepted above. Overall R5 is complete.

The final changes qualify total historical retention and separate captured
stream identity from severity:

| Source / change | Exact-source CI | Native cases per identity | Repetitions | SYSTEM / headless standard-user time | Equal-tree merge |
| --- | --- | --- | --- | --- | --- |
| `a38c88f269ea3a5903a737ad79f3e448195e170c`, [PR #215](https://github.com/PLN/winunitd/pull/215), historical disk retention | [34776212301](https://github.com/PLN/winunitd/actions/runs/34776212301) | 52 | 3 | 59.156s / 61.255s | `a4bc1433fe5f5233cc812fb850f847e07955eb9c` |
| `a576ef265a376a8a4a8b3b67782c5389333ab58e`, [PR #216](https://github.com/PLN/winunitd/pull/216), stream/severity separation | [34776680148](https://github.com/PLN/winunitd/actions/runs/34776680148) | 59 | 3 | 56.811s / 54.691s | `84740a71d254bbc5155211ca3fda3531b86b150a` |

Both disposable Windows 11 Enterprise LTSC build 26100 matrices ran with one
logical CPU, `GOMAXPROCS=1` and test parallelism one, with no skips. The final
matrix contains 43 journal, seven manager, one timer and eight notification
cases. Four clean-source non-race test binaries, module/toolchain hashes and
successful hosted Windows/Linux CI were verified for each source. Fixture,
process, profile and linger cleanup passed; the hosting broker stayed running.
Raw scripts, logs, measurements and manifests are retained privately. The test
binaries embed the candidate manager; this does not qualify a newly installed
broker or the MSI/pilot gates.

| Requirement | Bound and accepted evidence |
| --- | --- |
| Lines and continuation | Captured messages fragment at 64 KiB with UTF-8 reconstruction and continuation/partial metadata. Unterminated stdout/stderr drain before EOF. Fragment/schema cases pass in the final matrix. |
| Queue and fairness | 16 MiB / 16,384 records per store, including the in-flight write; 4 MiB / 12,288 records per invocation; 2048 queued groups. Round-robin service and displacement preserve quiet progress with explicit victim loss. [Combined manager pressure measurements](R1-EVIDENCE.md#combined-manager-pressure-measurements) qualify five noisy children plus a quiet child, sampled memory/worker growth, status latency and concurrent stop under stalled storage. |
| File ownership and counter memory | 1024 owned file records/handles/buffers; 2048 per-name statistics entries, with up to 1024 protected configuration/ownership names. Lifetime totals survive history and configuration removal. Churn, failed retirement, concurrent captures, stale sync and manager status cases pass in the final matrix. |
| Historical disk retention | 1 GiB including accepted buffered bytes, 8192 canonical current/archive filenames and a 16,384-entry initial scan limit. Only unowned historical units are evicted, ordered by their most recent generation activity. Buffered accounting, restart indexing, oversized index, alias protection, live-owner preservation and partial deletion retry pass. Real Windows sharing violations preserve locked history and recover after release. |
| Observable storage degradation | Drops, write errors and historical eviction have separate counters. Short/partial writes retain the exact unwritten suffix; repair retries without new output. Failed-close loss, tail repair and every rotation step pass again. Blocked storage-error formatting and retention deletion preserve queue/admission or independent sync progress. [Earlier actual volume exhaustion](R1-EVIDENCE.md#actual-volume-exhaustion) supplements these injected failures. |
| Compatible reads and severity | Journal v4 leaves raw stdout/stderr severity unknown, preserves stream/PID/invocation/user origin, and retains explicit daemon severity. Mixed v1-v4 reads preserve historical values and bytes. Native child output, system/user logs API and bounded supervisor diagnostic cases pass in the final matrix. The two new regressions fail against the old inference/version behavior. |

Both changes passed full local race suites, vet and Windows/Linux staticcheck.
Final full race times were journal 46.612s / manager 61.071s for retention and
journal 51.472s / manager 63.073s for stream/severity. A stale test-helper deadline
was corrected to give the later independent capture wait its own unchanged
one-second allowance; 30 constrained repetitions passed. Exact-source CI covers
the final retention retry check and all final test assertions.

The [operational contract](OPERATIONS.md#accepted-work-and-cancellation) states
admission and recovery limits. The disk budget is not a filesystem quota:
unrelated/external files and oversized preexisting history can require operator
repair. An overlarge initial index rejects writes visibly. Active or uncertain
file owners are never evicted for space. Rotation progress is retained in memory,
not an atomic multi-file power-loss transaction. Earlier resource measurements
are sampled maxima for the documented fixture, not universal process-memory
bounds. Combined trigger/reader/disk/crash stress is accepted above under R5.5.

## Timer delivery acceptance

R5.2 technical acceptance is complete as of September 13, 2026. The R2/R3
dependencies are satisfied. R5.3, R5.4, and R5.5 are accepted, and the complete
R5 gate is met.
The [delivery contract](OPERATIONS.md#timer-delivery-and-clock-domains) documents
relative and wall-clock origins, civil-time gaps/folds and active-service overlap;
the [persistence evidence](#timer-state-replacement-and-interrupted-activation)
establishes coalescing, interrupted activation retry and its duplicate possibility.

Two defects found during this qualification are fixed:

- [PR #209](https://github.com/PLN/winunitd/pull/209) keeps mixed timer clock
  domains independent. A forward wall jump previously fired an unelapsed
  boot/startup deadline, or recorded the wrong scheduled source. Four early-fire
  and two source-stamp regression cases reproduce on the previous implementation.
  Wall-clock delivery now leaves a later relative deadline unconsumed.
- [PR #210](https://github.com/PLN/winunitd/pull/210) selects the first civil-time
  occurrence using the actual timezone offset change. A one-hour assumption
  previously selected the second occurrence during Lord Howe's 30-minute fold;
  the permanent engine regression reproduces that error.

| Source | Exact-source CI | Native cases per identity | Repetitions | SYSTEM / headless standard-user time | Equal-tree merge |
| --- | --- | --- | --- | --- | --- |
| `6311cb1429bc3d02f990820fbd8cfb4eb2a3167b` | [34770433652](https://github.com/PLN/winunitd/actions/runs/34770433652) | 68 | 3 | 10.468s / 8.362s | `ea2a9da7457e8db80228bf614d495ec1ba2bf2d4` |
| `15942407303668886da884582131fb7bcabe546a` | [34771224682](https://github.com/PLN/winunitd/actions/runs/34771224682) | 90 | 3 | 13.305s / 10.006s | `469ab7075cf32f6f2dc83b1a93dc44c9fed7d15d` |

The final disposable Windows 11 Enterprise LTSC build 26100 matrix contains
56 manager, four journal, 26 timer and four notification tests, including the
earlier coordinator/storage/admission regressions. Every selected case passed
three times per identity without skips. Four native test-binary hashes, clean
source, module/toolchain identity and successful hosted Windows/Linux CI were
verified. Native binaries are non-race builds; race coverage is separate. Full
logs and manifests are retained privately. Final teardown removed the fixture,
disabled linger, unloaded the profile and left no fixture, user-manager or helper
process. The hosting broker remained running.

| Delivery requirement | Qualified behavior |
| --- | --- |
| Boot/startup origins | `TestWindowsOnBootSecVsOnStartupSec` proves an elapsed boot deadline fires while a ten-second startup deadline stays pending during the 400ms observation; `TestWindowsTimerOneshotOnStartupSec` separately proves actual 200ms startup delivery. Fake-clock origin and enabled-boot/reload cases also pass. |
| Clock jumps and mixed sources | Forward/backward calendar reconciliation, explicit clock notification, relative deadlines retained across wall jumps, and consumption of only the due source pass. Retry waits retain their elapsed-time budget and cannot cross a replacement arm. |
| DST | `TestEngineCalendarDeliversDSTGapAndFoldOnce` covers New York, Berlin and Lord Howe: first valid time after a gap on the same date, first fold occurrence only, no second copy, and next-day delivery. |
| Suspend/resume policy | `TestEngineResumeReconcilesAllClockDomains` delivers boot, startup, last-activation and calendar deadlines once after simulated resume; repeated notification does not redeliver. Existing last-activation suspend coverage also passes. |
| Persistent catch-up and overlap | `TestPersistentCalendarCatchupCoalescesWithActiveService` coalesces an initial 72-hour gap and a later 72-hour simulated resume while retaining the active service invocation and one launch. Each catch-up records a new completed activation identity and current time. |
| Crash recovery | Pending-intent retry, failed result publication, missed-calendar coalescing and month-end recovery pass again in the final matrix. The earlier file/process-crash qualification below remains applicable. Execution before durable result publication can repeat work; persistent workloads must be idempotent. |

The qualification clock was corrected as part of PR #210: pending waits use
elapsed uptime, wall jumps preserve remaining wait, simulated resume advances
Windows uptime, and a maximum-duration wait does not overflow. Both prior clock
model defects reproduce independently. Standalone test binaries embed timezone
data. Resume evidence establishes scheduler policy using controlled clocks under
both native identities; it does not claim an actual physical sleep/hibernate test
or qualify platform wake-notification delivery. The boot/startup tests above use
the real Windows clock and process launcher.

Final full uncached race tests passed (manager 63.049s, journal 41.112s, timers
3.253s), as did vet and Windows/Linux staticcheck. Twenty final DST/resume
repetitions passed in 2.021s. Earlier twenty complete timer-package and manager
clock matrices passed in 12.311s and 4.966s; the mixed-clock source also passed its
full race suite and twenty new regression repetitions. No timeout was relaxed.

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
per-name-counter bounds remain implemented. Total historical disk retention is
now accepted above. Durable daemon diagnostics and overall R5 are accepted in
[diagnostics and event resources](#diagnostics-and-event-resources).

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
filesystems. Delivery policy and journaling are now qualified above. Diagnostics
and overall R5 acceptance are complete. Installer state rollback stays a
separate R6 gate. R2/R3 technical acceptance is complete. A3 and R4.4 stay open.

## Diagnostics and event resources

R5.4 technical acceptance is complete as of September 22, 2026. R5.1–R5.5 are
checked, so overall R5 is complete.

R5.4a runtime diagnostics merged at `a48e6e9dbde9d7f093f37b6b9c0e4336bc03d5e8`
([#236](https://github.com/PLN/winunitd/pull/236)). That tree matches the
qualified source `480470064b085591b2cd57ab1bf4c3c51f021149`, which passed
[exact-source Windows/Linux CI](https://github.com/PLN/winunitd/actions/runs/35763808946).
Status and snapshot copy the restart budget. Timer storage and schedule state
stay on unit status and list-timers. The daemon log under the data root queues
allowlisted event codes, rejects a symlink or reparse point, and refuses a
resolved path outside that root. Drop and write-error counters appear on
machine status. Native SYSTEM and headless-user qualification of that tree
passed. Raw logs stay private.

The daemon-log ACL fix for [#253](https://github.com/PLN/winunitd/issues/253)
changes the DACL after #236 because user managers
could not open their diagnostics; this behavior was not present in that
previously qualified tree. On Windows the directory, current log and rotated
log use protected DACLs granting full access to the manager process user, SYSTEM and
Administrators; a LocalSystem manager retains the SYSTEM/Administrators-only
DACL. User managers therefore retain access to their own diagnostics across
creation, rotation and restart without granting other users access. The
root/reparse checks and nonblocking writer remain unchanged. Before changing
a DACL, the fix rejects owners other than the process user, SYSTEM or
Administrators, including a foreign owner that granted WRITE_DAC.

R5.4b event resources and the product MSI merged at
`59ec4d802f0346647eb39d45539fb84aa9cb5368`
([#238](https://github.com/PLN/winunitd/pull/238)). That tree matches the
qualified source `a3b5bb25388c49f46509af624a7e8cb778ecddb6`, which passed
[exact-source Windows/Linux CI](https://github.com/PLN/winunitd/actions/runs/35776115569).
The product MSI registers the Application source `winunitd` and embeds a
message table in `winunitd.exe`. Startup failures use the daemon log code
`daemon.startup-failed` and the same redaction rules. Environment assignments,
credential or store URIs, and parser dumps stay omitted.

Rendered Application events passed on a clean Windows installation.
Qualification id `a3b5bb2-a5-msi` records readable Application messages for
event IDs 1000 (daemon opened) and 1002 (startup failed). Raw logs stay
private. A3 and R4.4 stay open. R6.1 stays a separate gate.
