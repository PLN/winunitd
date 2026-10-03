# Installation

This is the product MSI for WinUnit Manager. It is separate from the unsigned
beta package in `packaging/beta`. The product UpgradeCode is
`A512B91F-1883-40FD-8EDB-5B8C5708DEEA`. Installer version `0.1.0` uses
ProductCode `78C43374-5AB7-4E81-B9CF-09E8ACD01133` and the human release
`0.1.0-alpha`. Publisher metadata is PLN.

The package is machine-wide and x64. The destination machine does not need
Go, WiX, .NET, or Python.

## Layout

| Path | Contents |
| --- | --- |
| `%ProgramFiles%\winunitd\bin` | `winunitd.exe`, `winctl.exe`, `winunit-notify.exe` |
| `%ProgramFiles%\winunitd\doc` | License, notices, this document, the unit reference, and examples |
| `%ProgramData%\winunitd` | `units`, `enabled`, `journal`, `runtime`, `linger`, `daemon` |

Examples are documentation only. The package does not enable them and does
not copy them into `units`. User managers create `%LOCALAPPDATA%\winunitd`
themselves. The MSI does not write user profiles.

Program Files grants modify access to SYSTEM and Administrators, and
read/execute to Users. Machine data grants control to SYSTEM and
Administrators only.

## Install, repair, and remove

Build `winunitd-0.1.0-x64.msi` with `packaging/wix/build.ps1` on a packaging
host. On the destination:

```text
msiexec /i winunitd-0.1.0-x64.msi /qn /norestart /L*v install.log
msiexec /fa winunitd-0.1.0-x64.msi /qn /norestart /L*v repair.log
msiexec /x winunitd-0.1.0-x64.msi /qn /norestart /L*v uninstall.log
```

The package does not add a custom setup wizard. Quiet install is `/qn`.
Basic Windows Installer UI is `/qb`. Do not force a reboot.

| Exit | Meaning |
| --- | --- |
| 0 | Success |
| 3010 | Success; a reboot is required. The package does not start one |
| 1602 | The operator canceled |
| 1603 | Fatal error, including a downgrade, the beta-package launch condition, or a preflight conflict such as the compatibility floor; the MSI log names the cause |
| 1618 | Another installation is already running; retry later |
| 1638 | Another version of this product is already installed; repair or upgrade it instead of installing beside it |

A newer product package produces the message "A newer package is installed."
The unsigned beta uses a different UpgradeCode. If that beta is present, this
product refuses to install beside it. Remove the beta first. Repair and
uninstall of this product are still allowed.

The PATH feature is installed by default and appends the `bin` directory.
Ownership is the `PathEntry` value under `HKLM\Software\PLN\winunitd`.
Opt out on first install:

```text
msiexec /i winunitd-0.1.0-x64.msi REMOVE=AddToPath /qn /norestart
```

Uninstall removes that owned PATH entry and leaves unrelated PATH entries.

## Service and events

The package registers the `winunitd` service (display name WinUnit Manager)
as LocalSystem, automatic start, with arguments pointing at
`%ProgramData%\winunitd`. It also registers the Application event source
`winunitd` against the message table in `winunitd.exe`.

Do not run `winunitd install` or `winunitd uninstall` on a machine that uses
this package. Those verbs are for a manual development service and do not
own PATH or the event source.

MSI recovery restarts the service three times, one second apart, including
failures that are not crashes. The failure count resets after 49710 days.
That is the largest day count the WiX utility extension can store. Manual
`winunitd install` still uses an infinite failure-count reset. The package
sets a 180-second preshutdown value.

## Servicing a running manager

Repair, upgrade, and uninstall run an embedded helper before Windows
Installer replaces or removes package files. Removal of an older product
is after `InstallExecute`, so the helper is not placed between
`InstallInitialize` and `RemoveExistingProducts`. The helper is stored
in the package. It is not an installed file, so removing the previous
product cannot delete the copy rollback still needs.

When the service is running, the helper asks the maintenance endpoint to
quiesce and allows 180 seconds. Any result other than `quiesced`, including
a missing endpoint, aborts the transaction before files are replaced. It
then stops the service and waits up to another 180 seconds for SCM to
report stopped and for that process to exit. Microsoft documents about a
30-second wait for `ServiceControl`. This package still authors
`ServiceControl` with `Wait="yes"`, and that action runs only after the
helper has already stopped the service. The 180-second budget is the
contract; the stock wait is a backstop.

After the stop, the helper opens `winunitd.exe`, `winctl.exe`, and
`winunit-notify.exe` without sharing. If one of those files is still open,
or the service process is still alive, replacement aborts. The MSI log
contains `abort replacement`.

Before that stop, the helper records start type, delayed start, recovery
actions, reset period, non-crash recovery, preshutdown timeout, recovery
command, reboot message, and whether the service was running. The record
is an administrator-only file directly under Program Files, outside the
product directory, and its name includes a transaction id created for that
install. Success deletes the record. On failure, after Windows Installer
restores files and service registration, the helper restores the recorded
configuration and the previous running or stopped state.

Restarting the manager after a successful upgrade starts enabled units.
Units that were started manually and are not enabled are not restored.

On a disposable machine, `WINUNITD_TEST_FAIL=1` fails the transaction after
the replacement service has started. That property is a qualification hook.
It is not part of ordinary install, repair, or removal. `msiexec /f` ignores
command-line properties; set the same name in the environment of the msiexec
process that launches repair, including a silent repair. The client sequence
copies it into the secure property. The elevated sequence leaves that value
in place when it is already set, and otherwise reads it from the launching
msiexec process. Repair can also use `/i` with `REINSTALL=ALL`
and `REINSTALLMODE`, as in the packaging spike. The package disables Restart
Manager so a live payload handle aborts in `service-prepare` instead of being
closed at `InstallValidate`. The verbose MSI log includes that helper's
standard error, including `abort replacement` and the locked file name.

## Data kept across repair, upgrade, and remove

Machine units, enable records, journals, timer state, linger records, and
`daemon.log` live under `%ProgramData%\winunitd`. User managers keep their
own trees under `%LOCALAPPDATA%\winunitd`. None of those files are MSI
file components. Repair and upgrade replace package binaries and metadata
only. They do not overwrite unit files, reset enable records, or recreate
user configuration. The mutable directories are permanent, so uninstall
does not remove them either. There is no purge option.

Uninstall does remove the service registration, the package binaries, the
documentation, the Application event source `winunitd`, and the PATH entry
this package added. Unrelated PATH entries stay.

Reinstalling over the retained tree starts whatever units are already
enabled. Units that were started manually and are not enabled are not
restored. That is the same rule as a successful upgrade.

The package does not rewrite unit files or on-disk state when a format
changes. Readers stay able to load the previous format. An explicit
migration is a separate transaction. Rolling the MSI back does not undo
that migration.

A manual install that uses a base directory other than
`%ProgramData%\winunitd` is not adopted or moved. The migration
command is [tools/migrate](../tools/migrate/README.md). It fail-closes
on that custom `--base-dir` instead of adopting it. R6.5 is that
command plus the disposable VM pilot fixture. R7 is the live Hermes
handoff and soak. This package only detects the conflict and stops.

## Conflicts and unsafe directories

Before the service is stopped, replaced, or started, the elevated helper
rejects the transaction when any of the following is true:

| Condition | Result |
| --- | --- |
| A `winunitd` service already exists and this product is not installed | Conflict. The package does not take ownership of a manual registration, even when the paths match |
| The service image is not `%ProgramFiles%\winunitd\bin\winunitd.exe` | Conflict. An unmanaged binary path is left unchanged |
| The service account is not LocalSystem | Conflict. An unrelated service of the same name is left unchanged |
| `--base-dir` is not `%ProgramData%\winunitd` | Conflict. A custom base directory is not adopted or relocated |
| A machine scheduled task or machine Run value launches `winunitd.exe` | Conflict on install, repair, and upgrade. Uninstall still removes this package. Per-user discovery stays with the explicit migration step |
| `%ProgramData%\winunitd` or `units`, `enabled`, `journal`, `runtime`, `linger`, or `daemon` is a reparse point, is not owned by SYSTEM or Administrators, or grants write access to another principal | Conflict. The helper does not follow the link and does not try to repair the target ACL |
| A [compatibility floor](#compatibility-floor) is set and this package's build is below it, or the floor record cannot be trusted | Conflict on install, repair, and upgrade, with `preflight conflict: compatibility floor:` and the reason. Uninstall still proceeds |

The MSI log contains `preflight conflict:` and the reason. Fix the
condition, or use the explicit migration path, and run the install again.
Repair does not reapply ACLs on the mutable directories, so a safe
existing ACL is left as it is. New directories inherit the data-root ACL,
which grants control to SYSTEM and Administrators only.

## Compatibility floor

A compatibility floor is the minimum build that may admit hosted work on the
machine. A consumer sets it when it admits workloads that need a release or
a feature, so that repair, upgrade, rollback, or recovery cannot bring back a
build those workloads cannot use. It is not raised automatically by an
upgrade; rolling back to an earlier package that still meets the floor stays
possible.

The floor is the file `%ProgramData%\winunitd\daemon\compat-floor.json`. It
survives repair, upgrade, and uninstall with the rest of the data tree. It is
trusted only when it is a regular file, owned by SYSTEM or Administrators,
whose DACL lets no other principal write it. Set and inspect it with the
installed daemon binary, as an administrator:

```text
winunitd floor set --min-version 0.2.0 --require exec-stop --require-clean
winunitd floor show
winunitd floor check
winunitd floor clear
```

| Field | Requirement |
| --- | --- |
| `minVersion` | The release is not lower, by semantic-version precedence (`0.1.0-alpha` is lower than `0.1.0`) |
| `requireFeatures` | The build lists every named capability feature |
| `requireCleanBuild` | The build has a known source revision from an unmodified tree; unknown state does not count as clean |

`set` refuses a floor that this binary does not meet. `check` exits 0 when the
binary meets the floor or none is set, and 1 when it is below the floor or
the record cannot be trusted. Usage errors exit 2. A changed floor applies at
the next manager start.

The floor is enforced in two places. Before it stops the running manager,
the package helper refuses to install, repair, or upgrade to a build below
the floor (exit 1603, `preflight conflict: compatibility floor:`), so the
installed manager and its workloads are left as they were. When the manager
starts, including after a failed upgrade rolls back, after a reboot, and after
SCM restarts it, it compares its own build with the floor before it starts
anything. A build below the floor, or an untrusted or malformed floor record,
holds admission: the control and maintenance endpoints stay available, but no
unit starts (boot, explicit, timer, watch, or recovery) and no user manager is
launched, so no user unit starts either. `winctl status` shows
`AdmissionHold` (`admissionHold` in status and snapshot JSON) and the daemon
log records `daemon.admission-held`. Only a manager restart after
restoring a compatible build, or after changing the floor, reopens admission.

The feature list that a floor can require comes from the capability query.
Builds that do not report it yet cannot meet a floor that requires features.
A build older than the first release with the floor cannot enforce it. The
package already refuses to install a lower version while the product is
installed; uninstalling and then installing such a build, or replacing files
by hand, is outside supported servicing. This behavior has not yet been
qualified on Windows.

## Servicing without a hosted controller

Servicing needs only local administrator rights and assets kept on the
machine or a protected share: the current and previous product MSIs with
their package manifests, outside the product directories. A hosted
controller is not involved.

1. Record `winctl status`, `winctl snapshot` (JSON), and
   `winunitd floor show`.
2. Run the repair, upgrade, or rollback package with `/qn /norestart
   /L*v <log>`. The package quiesces and stops the manager before it changes
   files and never forces a reboot.
3. Check the exit code. 3010 means success with a reboot still required;
   schedule it. For 1603, find the cause in the log. A failed upgrade rolls
   back to the previous package, service configuration, and running state.
4. Confirm that the service runs, that `winctl status` shows no
   `AdmissionHold`, that `winunitd floor check` exits 0, and that the
   installed files match the package manifest.
5. Confirm the units you depend on.

Global maintenance stops all system and user work and closes admission until
the manager restarts. The restarted manager starts enabled units. The
managers of lingering users and of users who are logged on start again with
their enabled user units; other users' managers start at their next logon.
Units that were started manually and are not enabled are not restored; start
them again if they are still needed.
A held manager starts nothing until admission reopens.

## Not in this package

The migration command is [tools/migrate](../tools/migrate/README.md).
R6.5 is that command plus the disposable VM pilot fixture. R7 is the
live Hermes handoff and soak and has not started. For 0.1-alpha, R6.1
and R6.4 are checked. The SYSTEM subset is `9961798-r64-accept` in
[R6 evidence](R6-EVIDENCE.md#acceptance), including `preflight-reparse`
and `preflight-unmanaged-service`. `gui-install`, `offline-install`,
`non-admin`, and `beta-conflict` passed under `7d21de2-r64-finish` on
an unclaimed Eval guest. deferred-media (Windows 11 Enterprise,
Windows 11 Enterprise LTSC non-Eval, Server 2022/2025, and Server
Core) and deferred-older-msi (`downgrade`, `n1-upgrade`, and
`rollback-upgrade`) were not run. Native R6.5 fixture evidence
`4223ac9-r65-migrate` is verified on tip
`f4e6c477ada211cf5df736a16ea7685a612412a4`. `-execute-msi` stays
unqualified. For 0.1-alpha only, overall R6 is checked under the
[#251](https://github.com/PLN/winunitd/issues/251) deferral policy. The documented
limitations are deferred-media and deferred-older-msi. R4 acceptance
(R4.1, R4.4, and R4.5) remains an unmet prerequisite for the fully qualified
release. This alpha check does not itself authorize the R7 handoff.
Servicing evidence for the quiesce and rollback transaction is in
[R6 evidence](R6-EVIDENCE.md#maintenance-and-rollback).
