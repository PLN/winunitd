# Pilot migration

This is the R6.5 operator command. It is a separate program from the MSI.
It is not a custom action, and it does not call `winunitd install` or
`winunitd uninstall`. The caller supplies the MSI path. The command does
not embed a package.

R6.5 qualifies the command on a disposable VM pilot fixture. That fixture
mimics a Task Scheduler interactive pilot plus a unit and enable tree. It
is not a live Hermes install. Live Hermes handoff and soak are R7.

Fixture accounts are alice, bob, and carol. The command does not collect
account secrets. Linger records stay in the pilot tree unless the operator
passes `-linger`. A custom `--base-dir` is a conflict. The command does
not adopt it.

## Flow

```text
go run ./tools/migrate -root FIXTURE init -scenario happy
go run ./tools/migrate -root FIXTURE discover
go run ./tools/migrate -root FIXTURE backup -out BACKUP
go run ./tools/migrate -root FIXTURE dry-run -msi CALLER.msi
go run ./tools/migrate -root FIXTURE apply -msi CALLER.msi -backup BACKUP
go run ./tools/migrate -root FIXTURE health
go run ./tools/migrate -root FIXTURE rollback -backup BACKUP
```

`discover` reports machine-wide MSI preflight rejects separately from
per-user moves. `-user=false` skips per-user discovery and blocks handoff
when a user pilot is present.

`backup` exports task definitions and copies the pilot unit, enable, and
journal tree into an operator-chosen directory outside the fixture. Apply
refuses to run when the fixture changed after that backup.

`dry-run` prints the disable, stop, copy, archive, install, and health
sequence. It does not mutate the fixture. Conflicts exit 2.

`apply` fail-closes on an unmanaged or non-LocalSystem `winunitd` service,
the beta UpgradeCode, an unsafe or reparse data directory, a destination
collision, a duplicate enabled `winunitd.exe` launcher, or a machine
scheduled task or Run value that launches `winunitd.exe`. It does not
overwrite an unrelated `winunitd` service. Machine launchers are reported
and left in place.

On a clean fixture, apply disables the pilot manager task and old triggers,
stops owned processes, copies units and enable records into the standard
user data tree, leaves journals in place (the backup holds the archive),
and records a product service identity when the caller-supplied MSI exists.
The default does not invoke msiexec. `-execute-msi` on Windows runs
`msiexec /i CALLER.msi /qn /norestart`. Success still requires the health
checks: product service identity, disabled pilot, stopped owned processes,
copied workload unit, and the control-owner marker that stands in for the
system pipe and `winctl`.

Failure after mutation restores the fixture from the backup and deletes
files the handoff added. The backup stays. `-fail-after` injects that
failure after `disable`, `stop`, `copy`, `install`, or `health`. The same
restore is `rollback -backup BACKUP`. Rollback stops a service that
`-execute-msi` started. It does not uninstall the package.

## Disposable VM fixture

`fixture/Invoke-MigrateFixture.ps1` builds a temporary stand-in and runs
discover, backup, dry-run, apply, and health, then an injected copy
failure that must restore the pilot, then a custom `--base-dir` dry-run
that must exit 2 without writes. It does not register real scheduled
tasks, call msiexec, or call `winunitd`.

```text
go build -o migrate.exe ./tools/migrate
powershell -NoProfile -ExecutionPolicy Bypass -File tools/migrate/fixture/Invoke-MigrateFixture.ps1 -MigrateExe migrate.exe
```

Native disposable-VM evidence is recorded in
[R6 evidence](../../docs/R6-EVIDENCE.md#4223ac9-r65-migrate). That run
passed on tip `f4e6c477ada211cf5df736a16ea7685a612412a4` with marker
`r65-fixture-pass`. `-execute-msi` stays unqualified.
