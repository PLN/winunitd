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
sets a 180-second preshutdown value. It does not yet wait 180 seconds before
replacing files. Stop the service before repair or upgrade.

## Data kept on remove

Uninstall removes the service, the event source, the binaries, the
documentation, and the owned PATH entry. It keeps
`%ProgramData%\winunitd` and per-user data, including units, enablement,
journals, timer state, linger records, and `daemon.log`. Reinstalling over
that tree starts whatever units are already enabled. There is no purge
option.

## Not in this package

Quiesce and rollback of a running service, data migration, and the install
matrix are later packaging work. Native SYSTEM installation has to be
qualified separately from this source tree.
