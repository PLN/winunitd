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

## Limits

This is the first R2.1 ownership slice, not the completed v2 coordinator. It does
not add revision identifiers, atomic candidate/graph acceptance, immutable
status snapshots, bounded operation admission, or typed completion events.
Timer and watch configuration capture remains separate work. Existing generation
checks and lifecycle writers remain in place; the full interleaving matrix and
source audit are still required. These adapter tests do not qualify real SCM or
Task Scheduler behavior, the supported Windows matrix, or MSI maintenance.
