# Lifecycle writer audit

September 13, 2026. The completed [R2 authority and worker audit](R2-COORDINATOR-AUDIT.md)
supersedes the incremental migration inventory formerly maintained here.
[Consolidated acceptance](R2-EVIDENCE.md#consolidated-coordinator-acceptance)
records clean source, hosted checks, native identities, invariant coverage and
remaining qualification limits for issues #96/#97.

## Current authority and migration boundary

Design section 2 selects mutex-serialized decision handlers as the R2 end-state.
`Manager.mu` serializes unit, configuration and operation decisions;
`UserHost.mu` serializes instance, session, admission and linger policy decisions.
Combined snapshots and shutdown acquire manager then user host. User-host
handlers never acquire the manager mutex. Workers report captured observations;
only current-owner decisions publish accepted lifecycle records.

Unit/SID gates and configuration/linger storage locks order effects without
becoming lifecycle authorities. Parsing, planning, process/token/handle I/O,
readiness/liveness probes, persistence and adapter error observation occur
outside decision locks. Independent units remain concurrent. Completion bypasses
ordinary command admission. Accepted work retains its slot and resources through
late completion; a response deadline cannot discard native ownership.

## Writer inventory

The [complete writer table](R2-COORDINATOR-AUDIT.md#review-method-and-authority)
includes lifecycle handlers, operation/configuration admission, shutdown transfer,
health/native observations, helper/journal cleanup and user-host worker metadata.
It distinguishes constructor initialization and private worker results from
accepted state. Callers and lock boundaries were reviewed alongside the
mechanical assignment/map-deletion/goroutine inventory.

Graph transactions publish each member through current record/generation/stop-
epoch decisions. Their final result is never applied as a lifecycle snapshot.
Timer arms and native triggers carry accepted origin identity; watch capacity
waits stay in the existing watch loop rather than allocating retry workers.
System snapshots copy units, active operations and user-host ownership under the
same authorities; encoding occurs after unlock. Native status overlays remain
separate observations and do not become lifecycle writers.

The [worker table](R2-COORDINATOR-AUDIT.md#worker-admission-retention-and-completion)
records numeric admission and retained completion bounds for every reviewed class.
Notification listeners admit at most 64 readers before PID authorization/spawn;
recovery admission precedes spawning and obsolete gate waits cancel. Journal
queue/timer-engine locks perform bounded memory work without storage-error
formatting or file I/O. Reserved stop/diagnostic/maintenance paths retain progress
under ordinary overload.

## Invariants and regression evidence

All seven Design section 3 invariants have an explicit
[test mapping](R2-COORDINATOR-AUDIT.md#invariant-and-sequence-matrix).
The final 61-case matrix passed three repetitions per SYSTEM/headless standard-
user identity at `4855ce8`, with no skips and full cleanup. Exact-source hosted
race suites, vet and Windows/Linux staticcheck passed. Existing R1/R3 native
ownership and conformance results remain complementary evidence.

## Audit procedure and completion gate

Future lifecycle changes must preserve the audited authority, current identity,
bounded admission and retained resources. Repeat the relevant writer/caller/I/O
review and invariant regressions when changing those paths. Text search or
handler extraction alone cannot establish acceptance.

This audit completes the R2 technical scope. It does not establish finite return
of an uncancellable native call, maximum-configuration resource guarantees, every
Windows identity/security mode, historical disk retention, full MSI servicing or
the actual pilot soak. Those retain the separate [milestone gates](MILESTONES.md).
