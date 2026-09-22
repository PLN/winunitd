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

There is no extra installer UI. Do not force a reboot.

| Exit | Meaning |
| --- | --- |
| 0 | Success |
| 3010 | Success; a reboot is required. The package does not start one |
| 1602 | The operator canceled |
| 1603 | Fatal error, including a downgrade or the beta-package launch condition |
| 1618 | Another installation is already running |

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
command-line properties; set the same name in the process environment for
repair, or use `/i` with `REINSTALL=ALL` and `REINSTALLMODE` as in the packaging
spike. The package disables Restart Manager so a live payload handle aborts in
`service-prepare` instead of being closed at `InstallValidate`.

## Data kept on remove

Uninstall removes the service, the event source, the binaries, the
documentation, and the owned PATH entry. It keeps
`%ProgramData%\winunitd` and per-user data, including units, enablement,
journals, timer state, linger records, and `daemon.log`. Reinstalling over
that tree starts whatever units are already enabled. There is no purge
option.

## Not in this package

Data migration and the platform install matrix are later packaging work.
Native SYSTEM qualification of install, repair, uninstall, and this
servicing transaction has not been recorded. What that run must prove is
in [R6 evidence](R6-EVIDENCE.md#maintenance-and-rollback).
