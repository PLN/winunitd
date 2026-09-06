# Beta candidate qualification

September 6, 2026. These results apply to the retained unsigned packages built
from clean source `1c56a57ae6001b247c1358a8c7395d734326fe1d`, with Go 1.27.1,
WiX 7.0.0, and .NET SDK 10.0.400. The proposed release package is 0.2.1-beta;
0.2.0 is the older package used to exercise upgrade and rollback.

| Package | Bytes | SHA256 |
| --- | ---: | --- |
| winunitd-0.2.0-x64-beta.msi | 15896576 | `d16285a729b6fb76c1eb8bc022572e3df8203a0a963ba7ccc96714f4128437ff` |
| winunitd-0.2.1-x64-beta.msi | 15888384 | `8ecabc2e40b03d81a3a683705969dd07b0685bb84ea544e92843d99902d6d141` |

## Product installation and workload checks

A fresh disposable Windows 11 Enterprise LTSC x64 guest, build 26100.9168,
ran [the checked-in fixture](../packaging/beta/test.ps1) as SYSTEM with no
default network route. Both phases passed, separated by an actual guest reboot.
Fixture SHA256:
`42cd24f87b40e3e0f5e578718e834854879d016b369601bb6ac7a9bd8c29e070`.

- Fresh install, SCM startup, and installed binary hashes/version.
- Configuration reload, enablement, target start/restart, workload crash recovery,
  and captured application output using the shipped examples.
- Rejection of an upgrade while the manager runs, leaving old payload intact.
- Ordinary repair after explicit stop.
- Deliberately failed major upgrade restoring the old payload, followed by
  explicit service recovery.
- Successful major upgrade preserving configuration and installing the new
  binary hashes/version.
- Reboot activation with a new workload invocation.
- Uninstall removing the service/binaries while retaining configuration/journal;
  reinstall reusing enablement; final uninstall and independent absence checks.

The initial exploratory run found a missing empty unit directory in the package
and a log assertion that ran before the restarted application produced output.
Both were corrected before the complete clean rerun above. Raw logs, deployment
mappings, and controller records are retained privately. The guest and its disks
were retired after evidence export.

## Existing interactive application pilot

The exact 0.2.1-beta payload hashes were verified before replacing the existing
pilot binaries. Recovery was disabled during replacement, the manager and its
workloads stopped, and old binaries/task definitions retained for rollback.
No application update, unit migration, or system-service installation occurred
on this deployment.

All three application components recovered under one manager. The external
Hermes maintenance worker then passed its read-only `Rehearse` path in about
17 seconds: stop, quiescence, updater plan, disabled legacy task restoration,
restart, listener ancestry, and unchanged Task Scheduler process. The agent
responded after recovery and independently checked the CLI version and active
units. This qualifies that existing pilot topology, not general MSI user-mode
admission, logoff/linger, or arbitrary application identities.

## CI and limits

Routine Windows/Linux CI runs on trusted private Gitea runners; installation
checks run on disposable lab guests. No hosted GitHub run was triggered for
this packaging work. The initial implementation passed both CI lanes. A later
Windows run exposed an intermittent pre-existing shutdown fault-test failure:
the test restricted all captured handles but repaired only the intended child.
Commit `ce58b56` restricts only that child's handle and restores the retained
original handle. Twenty local repetitions and the subsequent full Windows/Linux
Gitea CI run passed, including race tests, vet, vulnerability scans, the nested
pipe regressions, maintenance tests, and builds. This changes tests only;
the qualified package source and bytes above remain fixed.

This is focused beta qualification, not an exhaustive hardware/identity matrix
or a long soak. Non-LTSC Enterprise builds, automatic daemon recovery, graceful
application stop, advanced unit types, and general interactive user admission
remain outside this evidence. Packages are unsigned; signing enrollment is
separate. See [installation and recovery](../packaging/beta/INSTALL.md).
