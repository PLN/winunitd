# R2 initial evidence

## Service definition ownership

A valid reload previously replaced the definition used to address an existing
native SCM service or scheduled task. The retargeting regression fails against
the preceding implementation: status queries the new, inactive target even
though the old target remains running. Stop could then leave that old target
running. Changing a process service into a native proxy had the same routing
problem.

Service launch now captures its definition before adapter side effects. Reload
continues to load the latest definition separately. Status overlays, resource
limits in status, stop/shutdown routing, exit/watchdog cleanup, and automatic
recovery consult the captured definition. Successful explicit cleanup releases
it. Failed native start or stop does not authorize abandoning the old target;
an explicit start targeting a different native identity requires stop first.
A fresh explicit process start adopts the latest definition after prior process
cleanup succeeds. Automatic recovery retains the original service configuration,
including its restart policy and start-limit settings.

The Windows manager tests use isolated fake SCM/task adapters and process
fixtures. They cover:

- Valid target replacement for both native proxy types, followed by stop and a
  fresh start using the new target.
- Every directed change between process, SCM, and scheduled-task service types,
  with list status, explicit stop/start, and shutdown.
- A native start that fails after starting its resource, a failed first stop,
  rejection of replacement starts, and successful cleanup retry.
- Reload while a native start is paused inside its adapter, followed by cleanup
  of the original resource after the adapter completes.
- Process exit after reload: automatic recovery launches the old command despite
  the new definition disabling restart; explicit stop/start launches the new one.

## Validation

Twenty race-enabled repetitions of the five reload ownership tests pass on the
local Windows development environment. The full repository race suite
(`go test -race -parallel 1 ./... -timeout 180s`) and `go vet ./...` pass. The
paused-launch regression was added afterward and passed in the focused twenty-run
set. No live pilot deployment was changed for these checks.

## Start plan definitions and stop precedence

Start plans now capture the member definitions alongside the dependency graph,
under the same manager lock. Pending members retain their runtime records until
the transaction finishes. A later valid reload cannot substitute a new command
into an already accepted graph; a fresh start plan uses the new definition.
Current alpha removal/invalid-file admission still rejects a missing or
unavailable member before launching it.

Each planned member captures a stop epoch. Stop admission invalidates older
pending starts, and transaction result application excludes those invalidated
members. A superseded transaction returns an error without overwriting the
completed stop's state or error. A start requested after that stop remains
permitted. This changes the older mid-transaction stop regression's result:
the canceled pending target is now reported as canceled instead of successful.

Twenty race repetitions cover a blocked dependency spanning valid reload, stop
of a member waiting for its dependency, and stop of a member that has already
started while another branch is pending. Definitions and stop epochs are internal;
this change does not introduce revision IDs or an operation-query API.

## Per-unit completion publication

A blocked multi-unit start reproduced a stale result overwrite: one process
exited and reached failed state, then completion of the remaining dependency
made the transaction summary mark the exited unit active. Start adapter outcomes
are now published per member while its operation gate is still held. Final
transaction application only handles members never launched and still matching
their original generation and stop epoch. It cannot replay completed members or
clear errors produced by a later exit/recovery operation.

A corresponding stop regression stopped one member, paused another member's
cleanup, and failed a new start of the already-stopped member. The old stop
summary incorrectly replaced the new failed state with inactive. Stop and
shutdown now leave runtime publication to their existing generation-checked
per-unit stop operations; transaction summaries are returned as outcomes only.
Both deterministic regressions pass twenty race repetitions. Graph planning and
dependency failure reporting remain separate from observed runtime state.

## Watch definition and callback ownership

Armed registry, event-log, and path watches retain the definition used to open
them. A PathExists regression reproduced evaluating new conditions against old
watch handles after reload. The captured conditions remain in effect until a
fresh activation opens replacement watches.

Callbacks carry the exact watch instance and generation. A stale existence
probe or failure cannot update or close replacement watches. Watch-originated
start plans validate that source at admission and again after waiting for the
destination operation gate; stopping the source cancels a queued companion
launch. Initial already-satisfied PathExists activation uses the same origin
checks. Four focused regressions pass twenty race repetitions using isolated
fake watches, delayed probes, and a blocked destination gate/launcher. A source
stop preserves an already-admitted companion launch and its result.

Native path testing also exposed an invalid test assumption: several filesystem
notifications from one write can admit another start after a companion stop.
Diagnostics confirmed a newer generation with stopping cleared. The native test
now stops the watch before asserting a stable stopped companion, rearms it, and
requires an additional counted execution after the next write. Twenty Windows
race repetitions pass.

## Dependency qualification finding

The full manager run uncovered a go-winio listener-close hang during watchdog
recovery. The accepted repository-relative patched copy passes 100 watchdog/restart
repetitions, the manager race suite, vet, build, and vulnerability scanning.
Its deterministic cancellation regression passes 100 repetitions and fails when
the original cancellation branch is restored. See the accepted
[pipe-listener decision](PIPE-LISTENER-DECISION.md) and upstream tracking.
The full-suite rerun also exposed a journal test deadline expiring before scan
admission. That bounded-worker test now cancels after confirmed admission; the
separate deadline/flush-lock regression remains. The corrected full repository
race suite passes with the checked-in replacement. Lab artifact admission also
accepts the new manifest and requires its dependency hash and license artifact.

## Timer deadline ownership

A dequeued timer deadline now carries its armed-instance identity and schedule
generation through consumption. Schedule generations are unique across rearming
the same name, so an old heap entry cannot match a replacement timer either.
Regressions reproduced both stale-dequeue consumption and an old calendar entry
firing a replacement schedule early. The replacement's real deadline still fires.
Twenty race repetitions of the complete timer package pass.

Timer callbacks now carry a unique arm token and the captured companion name.
Stop/rearm or refresh invalidates an old callback and its eventual success result;
an old result cannot write the replacement's persistent last-success timestamp.
Watch and timer starts share origin checks at plan and adapter admission. Stopping
a timer cancels a queued companion but preserves one already admitted to launch.
Valid reload retains the armed timer's schedule and target; a fresh activation
adopts the latest definition. Three manager regressions and the timer package
pass twenty race repetitions, including a controlled callback completion after
replacement and reload across two different relative timer schedules. The full
repository race suite and vet also pass after this callback-ownership slice.

## Limits

This is the first R2.1 ownership slice, not the completed v2 coordinator. It does
not add revision identifiers, atomic candidate/graph acceptance, immutable
status snapshots, bounded operation admission, or typed completion events.
Existing generation
checks and lifecycle writers remain in place; the full interleaving matrix and
source audit are still required. These adapter tests do not qualify real SCM or
Task Scheduler behavior, the supported Windows matrix, or MSI maintenance.
