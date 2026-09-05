# R0.5 packaging spike

September 5, 2026. Selected experiment tooling: **WixToolset.Sdk 7.0.0**, **WixToolset.Util.wixext 7.0.0**, and **.NET SDK 10.0.400**, targeting an x64 MSI. Installed winunitd binaries remain native Go executables without a .NET prerequisite.

winunitd remains MIT licensed. Packaging must not introduce a requirement to relicense its application code under a copyleft license. Assess build-tool terms separately from licenses and notices for any runtime components distributed in the MSI.

## Tooling decision

WiX v7 binary releases require explicit EULA acceptance. The [WiX binary-release agreement](https://github.com/wixtoolset/wix/blob/v7.0.0/OSMFEULA.txt) applies its maintenance fee to revenue-generating use at annual gross revenue of at least US$10,000. See also [FireGiant's explanation and acceptance mechanism](https://docs.firegiant.com/wix/osmf/). The project being free does not alone establish the status of the organization using the build tool.

The maintainer confirmed that the current use is exempt from the maintenance fee and authorized the binary SDK. The fixture build passes the explicit `AcceptEula=wix7` property. No paid subscription, sponsorship, or account enrollment was performed. Other contributors and CI operators must assess the terms for their own use; the repository's MIT license alone does not establish an exemption.

The agreement also permits source-built tools under the source license, with additional build/maintenance obligations. Do not fall back silently to an unsupported older WiX release.

## Experiment

Use a disposable guest and a dedicated fixture service whose name and data directories cannot collide with winunitd:

1. Build a minimal LocalSystem service that records start, stop, and preshutdown events and supports an explicitly configured slow stop.
2. Package two versions with stable upgrade identity and component ownership; author delayed automatic startup, recovery actions, and preshutdown configuration. Inspect the installed SCM state rather than trusting MSI authoring alone.
3. Install, start, repair, and uninstall the fixture; verify binaries, registration, privileges, and the documented retained-data boundary.
4. Exercise a stop longer than the normal MSI service-control wait. Measure native behavior and determine whether a narrowly scoped rollback-aware helper is required for the planned stop budget.
5. Upgrade from the older package while running, then inject failure before transaction commit. Verify rollback restores the previous package and service state without duplicate processes or unbounded waits.
6. Collect MSI logs, SCM state, process observations, identities, exact package hashes, and timings outside the guest. Redact before publishing evidence.

## Repeating the spike

Build on Windows from the repository root:

```powershell
./packaging/spike/build.ps1 -Mode helper
```

The script produces `dist/msi-spike/0.0.1/fixture-0.0.1.msi` and the corresponding `0.0.2` package. `-Mode native` reproduces native service configuration; `-Mode util` changes recovery actions to the pinned utility extension. Modes overwrite those output paths: retain hashes and copies before comparing modes. SDK intermediates are separated by mode/version. Keep `.wixpdb`, build logs, and raw MSI logs private because they can contain build or guest paths.

Transfer both packages into one directory in a disposable Windows guest, verify SHA256, and run as SYSTEM or an elevated administrator:

```powershell
./packaging/spike/native-test.ps1 -PackageDirectory C:\lab\packages `
    -EvidenceDirectory C:\lab\evidence -DisposableLab -QualifyHelper
```

Omit `-QualifyHelper` when collecting native/util behavior; those modes intentionally expose defects. The strict helper lane asserts success/failure codes, prior binary hash and running/stopped state after rollback, delayed startup, non-crash recovery, infinite recovery-reset period, preshutdown timeout, and fixture removal. It exercises a 45-second stop and a 200-second stop against the helper's 180-second deadline. A harness failure preserves the fixture for inspection; do not rerun against an existing service without resolving it.

The fixture owns only `winunitd-msi-fixture`, its Program Files directory, and transaction-specific rollback files under Program Files. It deliberately retains event and delay files for inspection on uninstall. It is not a winunitd installer and must not be installed on a production or developer machine.

## Observations and helper strategy

Initial runs used Server 2025 Standard Evaluation Core, build 26100.32230, through guest-agent execution as SYSTEM:

- Native WiX authoring rejects both `4294967295` and `-1` for the recovery-reset period. A one-day period was used to continue the experiment.
- Native `ServiceConfigFailureActions` failed installation with MSI 1939 / Windows error 5. The transaction rolled back and removed the fixture service.
- Replacing only recovery actions with `util:ServiceConfig` allowed installation and repair. Delayed startup, non-crash recovery, and preshutdown configuration could be queried back.
- The utility/native mix restored the old executable and running state after injected upgrade failure, but lost delayed startup and preshutdown settings. Declarative authoring alone did not preserve the required rollback contract in this scenario.
- A checkpoint-reporting 45-second stop completed through native service control in about 48 seconds on this guest. This is an observed exception to treating the documented MSI wait as a universal hard cutoff; it does not establish a portable 180-second stop guarantee.
- The embedded helper prototype restores the selected previous service settings and prior running/stopped state. It sets the actual infinite recovery-reset value through the Windows API, saves state before stopping, and waits for both SCM stopped state and process termination before replacement. Deferred rollback runs after MSI restores files/registration; a commit action removes saved state.

Production direction: use a narrowly scoped native helper for advanced settings and bounded maintenance, with explicit rollback state. Keep binary/service/component ownership in MSI. The prototype's late major-upgrade removal preserves component identity; production authoring must respect all associated component rules and test rollback after old-product removal.

The prototype requires a fresh transaction token from the lab harness. `msiexec /f` did not pass that custom property; fixture repair uses `/i REINSTALL=ALL REINSTALLMODE=amus`. The production MSI must generate its own transaction identity internally and support ordinary repair/uninstall entry points. This fixture does not qualify production custom-action security, crash/power-loss recovery, arbitrary service configurations, GUI servicing, signing, or the application maintenance barrier.

## Qualified fixture evidence

Implementation: `ea7fb358581ba7cfe54cdfc04bc99044c8ce14aa`. A clean build with Go 1.27.1, WiX 7.0.0, and .NET SDK 10.0.400 was transferred, hash-verified, and tested as SYSTEM on the Server Core build above. The strict helper lane passed:

| Scenario | MSI exit | Observation |
| --- | --- | --- |
| Install | 0 | Required settings and running service; 2.31 seconds |
| Repair | 0 | Required settings retained; 2.84 seconds |
| Failure after old-product removal | 1603, expected | Old binary, settings, and running state restored; 4.21 seconds |
| Same failure, previously stopped | 1603, expected | Old binary and settings restored; service remained stopped; 3.78 seconds |
| 200-second stop against 180-second deadline | 1603, expected | Prepare action failed at about 180 seconds; rollback waited for stop completion and restored the old running service; 200.94 seconds total |
| 45-second stop and upgrade | 0 | New binary running with required settings; 47.00 seconds |
| Uninstall | 0 | Service, executable, process, and transaction files absent; retained fixture logs remained; 0.97 seconds |

| Qualified artifact | SHA256 |
| --- | --- |
| fixture-0.0.1.msi | `e225fcdf5011526168d6871b1c44477de59a0ed35587d317784ddbad0d41046c` |
| fixture-0.0.2.msi | `ab6ee1f4ecec635e4ef0213f3837d35427aa1be3e26a377f868b43d25fc19fab` |
| 0.0.1 fixture.exe | `401f436b5d4b082debd8d8d6c0cc966ada4ef5325d6f01adea5f039161780052` |
| 0.0.2 fixture.exe | `7606035ba6fb7ea42b67ce091fa6545af55c65309835e56070c61e13524adfdf` |

The same scenarios also passed before the clean-commit rebuild. Raw logs, package copies, and the build manifest are retained in the private operator workspace. R0.5's experiment is complete; none of these results qualifies the product MSI or closes R0's remaining lab-controller gate.
