# Internal installer qualification

## September 13 startup and recovery policy

PR #177 source `dd12c1b9d2436066579dfa4d65a424019fbb718c` passed
[exact-source Windows/Linux CI](https://github.com/PLN/winunitd/actions/runs/34754375316)
and merged with the same tree. Manual and MSI installation now use ordinary
automatic startup, three one-second recovery restarts, an infinite reset period,
recovery for non-crash failures and a 180-second preshutdown timeout.
This fixes [consumer startup feedback #173](https://github.com/PLN/winunitd/issues/173).

The MSI helper captures the previous native policy before servicing, applies the
shared policy after registration and restores the captured policy on rollback.
Each transaction uses a fresh native random token and an exclusive, bounded,
SYSTEM/Administrators-only state file. Missing rollback state is a no-op when
preflight rejected servicing before preparation. Failed restoration retains its
state for diagnosis. Running-service servicing remains rejected.

On the disposable Windows 11 Enterprise LTSC baseline, build 26100, twenty
native SYSTEM repetitions verified policy application, idempotence, restoration
of a deliberately different policy, empty recovery actions and protected state
validation. No selected case was skipped; all temporary service fixtures were
removed.

A clean-source 0.2.3/0.2.4 MSI pair passed eight servicing phases: install,
rejection of running repair, stopped repair, injected upgrade rollback, upgrade,
uninstall, reinstall and final uninstall. The injected failure restored old
payload bytes, stopped service state and the deliberately different delayed-start
and recovery settings. Manual installation afterward selected the same new
startup/recovery policy. Package manifests, payload hashes and complete MSI
logs are retained privately with the source identity.

A real reboot after MSI installation admitted an interactive standard user and
started its enabled workload. Broker control was first observed ready within
20.713 seconds of boot; the admitted user's workload was first observed ready
within 20.993 seconds. These are observation upper bounds on this guest, not
exact transition times or a performance comparison with the consumer's host.
The user workload ran unelevated in session 1. Final cleanup restored the
previous lab broker and user startup settings, removed fixture ownership and
confirmed profile unload. The real application pilot was unchanged.

This qualifies startup/recovery policy and the listed servicing paths. It does
not complete R6. R6.3 data migration, R6.4 platform acceptance, R6.5 migration
tooling, and overall R6 remain open. R7 handoff and its seven-day soak have
not started.

## Maintenance and rollback

The product MSI now embeds the servicing helper described in
[installation](INSTALLATION.md#servicing-a-running-manager) and
[#241](https://github.com/PLN/winunitd/issues/241). Repair, upgrade, and
uninstall quiesce a running manager, then wait up to 180 seconds for SCM stop
and process exit before replacing files. That wait is longer than the
documented 30-second `ServiceControl` wait. `ServiceControl` stays in the
package and runs after the helper. An open payload file or a live service
process aborts replacement. Injected failure restores the captured service
configuration and the previous running or stopped state. Quiet install and
the Application event source `winunitd` are unchanged.

No successful native SYSTEM run of this transaction is recorded here. A
fresh SYSTEM install of `3ddf5cf` returned 1603 with MSI error 2613 because
custom actions sat between `InstallInitialize` and `RemoveExistingProducts`.
The package now schedules removal after `InstallExecute` and the helper
before `StopServices`. The cases below remain unproven. Raw MSI logs stay
in the private operator workspace. Do not add guest names, addresses, or
private paths to this note.

Native SYSTEM qualification must prove all of the following on a disposable
machine, as SYSTEM, from a clean build of the PR tip. Skipped cases do not
count.

1. Fresh install with no `winunitd` service returns 0. The service is
   running, the Application event source `winunitd` is registered, and the
   transaction state file under Program Files is gone.
2. Repair or upgrade while the manager is running a workload that keeps
   maintenance in progress for more than 30 seconds and less than 180
   seconds. The install waits past 30 seconds, then replaces files, and the
   service is running afterward. The MSI log shows `service-prepare` before
   `InstallFiles` and does not report error 2613. `RemoveExistingProducts`
   is after `InstallExecute`.
3. A workload that does not release within 180 seconds. The install returns
   1603, the log contains `abort replacement`, and the previous payload and
   running service remain.
4. A payload binary held open after the service would otherwise stop. The
   install returns 1603, the log contains `abort replacement` and the file
   name, and that binary's bytes are unchanged.
5. Upgrade and repair with `WINUNITD_TEST_FAIL=1`. Each returns 1603. The
   previous payload hash is restored. Start type, delayed start, recovery
   actions, reset period, non-crash recovery, preshutdown timeout, and the
   previous running or stopped state match the pre-transaction service.
   Repeat once with the service stopped beforehand and confirm it stays
   stopped.
6. Quiet repair without the test property still registers the Application
   event source and does not show installer UI.

Record MSI exit codes, the before/after running state, and payload hashes.
This qualification does not close R6.1, R6.3, R6.4, R6.5, or overall R6.
