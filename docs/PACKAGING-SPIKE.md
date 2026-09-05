# R0.5 packaging spike preparation

September 5, 2026. Proposed candidates: **WixToolset.Sdk 7.0.0** and **.NET SDK 10.0.400** on the build machine, targeting an x64 MSI. These candidates are not adopted or experimentally qualified yet. Installed winunitd binaries remain native Go executables without a .NET prerequisite.

## Tooling decision pending

WiX v7 binary releases require explicit EULA acceptance. The [WiX binary-release agreement](https://github.com/wixtoolset/wix/blob/v7.0.0/OSMFEULA.txt) applies its maintenance fee to revenue-generating use at annual gross revenue of at least US$10,000. See also [FireGiant's explanation and acceptance mechanism](https://docs.firegiant.com/wix/osmf/). The project being free does not alone establish the status of the organization using the build tool.

Confirm whether this is personal non-revenue-generating work or use within a revenue-generating activity before adopting the binary SDK. Do not put personal financial details or organizational identity in this repository. No fee, sponsorship, account enrollment, or EULA acceptance has been performed.

If applicable fees are acceptable or the use is exempt, use the pinned supported SDK. The agreement also permits source-built tools under the source license; that is an alternative with additional build/maintenance obligations, not a reason to assume the binary-release terms do not apply. Do not fall back silently to an unsupported older WiX release.

## Concrete experiment after selection

Use a disposable guest and a dedicated fixture service whose name and data directories cannot collide with winunitd:

1. Build a minimal LocalSystem service that records start, stop, and preshutdown events and supports an explicitly configured slow stop.
2. Package two versions with stable upgrade identity and component ownership; author delayed automatic startup, recovery actions, and preshutdown configuration. Inspect the installed SCM state rather than trusting MSI authoring alone.
3. Install, start, repair, and uninstall the fixture; verify binaries, registration, privileges, and the documented retained-data boundary.
4. Exercise a stop longer than the normal MSI service-control wait. Measure native behavior and determine whether a narrowly scoped rollback-aware helper is required for the planned stop budget.
5. Upgrade from the older package while running, then inject failure before transaction commit. Verify rollback restores the previous package and service state without duplicate processes or unbounded waits.
6. Collect MSI logs, SCM state, process observations, identities, exact package hashes, and timings outside the guest. Redact before publishing evidence.

No MSI, servicing experiment, or passing packaging result is claimed by this preparation. R0.5 remains open until the experiment is executed and its helper strategy is supported by observations.
