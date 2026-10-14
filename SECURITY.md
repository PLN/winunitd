# Security policy

## Reporting a vulnerability

Report vulnerabilities privately through GitHub's
[private vulnerability reporting](https://github.com/PLN/winunitd/security/advisories/new)
(the Security tab, "Report a vulnerability"). Do not open a public issue for
them.

Include the version (`winctl --version`), the Windows edition and build, the
steps to reproduce, and what an attacker gains. Leave out machine names, user
names and other private details unless they matter for the report.

This is a small project without a bounty. The maintainer aims to acknowledge a
report within a week. Fixes ship in the next release, and the advisory is
published with it.

## Supported versions

Only the newest release receives fixes. The unsigned 0.2.x beta in
`packaging/beta` is a separate, superseded package; move to the product MSI.

## Security model

winunitd runs as a LocalSystem service. These boundaries matter for reports:

- Administrators are trusted. They control the system units, the machine data
  under `%ProgramData%\winunitd` and the admission policy. System units run
  with SYSTEM privileges, by design.
- A standard user controls only their own user units, through a user manager
  that runs with that user's token. An administrator admits each user
  explicitly unless the machine opts into
  [file-based delegation](docs/USER-ADMISSION.md#file-based-delegation).
- The control pipes check the caller's identity: one user cannot control
  another user's manager or the system manager.

A way for a standard user to cross one of these boundaries, run code as SYSTEM
or another user, or read another user's units, logs or data is in scope.
Behaviour that needs administrator rights to begin with is not. The
[R4 evidence](docs/R4-EVIDENCE.md) records how the boundaries were tested.
