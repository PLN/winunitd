# Hermes user-pilot maintenance

`hermes-pilot.ps1` coordinates maintenance of an existing, directly launched
interactive-user pilot. This is operator tooling, not the future system-daemon
maintenance API or a general MSI updater. It does not replace winunitd binaries.

The worker must run in a separate, on-demand Windows scheduled task under the
same interactive account, with the privileges needed to export/remove/restore
the existing task definitions. Do not launch the worker as a child of Hermes.
The client only queues a fixed action and starts the task; it can then exit.
An optional delay allows an agent to finish its response before interruption.

Configure absolute `PilotRoot`, `HermesHome`, `ProjectRoot`, `WebUIHome`, `Python`
and `StateDir` paths, exact `PilotTask` and `MaintenanceTask` names, three
`LegacyTasks`, three `Units` in shutdown order, and expected listener `Ports`.
Keep this machine-specific JSON and all run output outside the repository.
`Python` must be a suitable base interpreter outside the mutable virtual
environment. The worker constructs the existing project's virtual-environment
module path. Review this assumption when changing the application's layout.

Register the fixed worker action as:

```text
powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File <script> -Config <private-config> -Worker
```

Use an interactive principal for the pilot account, highest run level when
required by task permissions, `IgnoreNew`, no recurring trigger, and an
execution limit longer than the bounded backup/update/recovery operations.
Protect the script, configuration, queue and backups from other users. This
same-user helper is not a security boundary against the account that owns it.

## Operation

Client actions are `Plan`, `Status`, `Rehearse`, `FailAfterStop`, `Update` and
`Recover`. For example:

```powershell
.\hermes-pilot.ps1 -Config C:\Ops\pilot.json -Action Rehearse -DelaySeconds 60
.\hermes-pilot.ps1 -Config C:\Ops\pilot.json -Action Status
```

The worker records source revision, dirty status, binary hashes, scheduler PID,
and task XML. It disables pilot recovery, explicitly stops the workloads,
stops the pilot manager, verifies no workload processes/listeners remain, and
temporarily unregisters the three previously disabled legacy tasks. Unexpected
Hermes tasks or Startup entries block the updater. The Windows Task Scheduler
service is never stopped or disabled.

`Rehearse` runs the stock updater's read-only `--plan` in this offline state.
`FailAfterStop` injects failure after quiescence to exercise restoration.
`Update` copies the offline Hermes home (excluding earlier backup archives and
directory junctions) into the private run directory, then runs the stock
`hermes update --yes --backup`. This preserves the application's normal
dependency/config migration and stash handling. There are no force or fake
no-restart flags. Confirm the chosen source branch and local modifications
before applying an update; this helper does not pin the upstream revision.

After successful updater exit it requires no unexpected runtimes/autostart,
restores legacy tasks disabled, restores pilot recovery, and checks loaded/active
units, one manager, listener ancestry and the unchanged scheduler PID.
Application authentication and an actual agent response must be checked after
this infrastructure verification; a listener alone is not application health.

Each request has a private run directory with durable phase/result JSON and
separate native stdout/stderr logs. A lock prevents concurrent workers and a
pending request blocks a second transaction. Backup retention is manual.

## Failure and recovery

Failures before update mutation attempt to restore service. `Recover` retries
a pending pre-mutation transaction after a worker interruption; it refuses if
update mutation had started. Recovery never automatically restores code alone
across a possible data migration. After mutation failure, inspect the retained
logs, installed state, updater receipt and backup before repair or rollback.
The request remains pending to prevent another update from starting.

Do not re-enable legacy tasks as a shortcut: that creates competing ownership.
Do not use the helper to stop shared Windows services, bypass unrelated process
holders, or update a different topology. This initial implementation covers
one interactive pilot and one Hermes installation, not a multi-profile fleet.

## Tests

Run `powershell -NoProfile -ExecutionPolicy Bypass -File hermes-pilot-tests.ps1`.
The tests exercise transaction ordering, pre-mutation restoration, failed-update
hold behavior, durable result writes and real native exit-code propagation.
Live rehearsal and injected-failure results belong in private operator evidence;
mocked transaction tests do not qualify native task or process behavior.
