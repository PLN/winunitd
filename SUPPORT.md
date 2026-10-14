# Support

## Getting help

Open a [GitHub issue](https://github.com/PLN/winunitd/issues) for a bug or a
question; search the existing issues first. This is a small project without
guaranteed response times. Report security problems privately as described in
[SECURITY.md](SECURITY.md).

Include what helps to reproduce the problem:

- `winctl --version` and the Windows edition and build (`winver`).
- `winctl status`, and `winctl status <unit>` and `winctl logs <unit>` for the
  affected unit. Add `--user` for a user unit.
- The unit file.
- The daemon log, `%ProgramData%\winunitd\daemon\*.log`, and Application log
  events from the `winunitd` source around the time of the problem.
- For installation problems, the MSI log (`msiexec ... /L*v install.log`).

Remove machine names, user names, addresses and anything else private before
posting.

## Supported configurations

| Area | Supported | Not supported or not tested |
| --- | --- | --- |
| Windows | Windows 11 Enterprise and Enterprise LTSC, x64. Tested on Enterprise LTSC build 26100. | Other editions, ARM64, Windows 10. Windows Server 2022 and 2025 are candidates and not yet tested. |
| Installation | The product MSI, per machine ([installation](docs/INSTALLATION.md)). | The unsigned 0.2.x beta is a separate, superseded package. Remove it before installing the product MSI. |
| Accounts | Local accounts and Active Directory domain accounts, including roaming profiles. | Microsoft Entra ID (cloud) accounts. Folder redirection is not tested. |
| Managers | The system manager; user managers in the user's session; linger (`enable-linger`), which runs a user manager without a sign-in. | The `headless-store-uri` linger mode is experimental. |
| Units | The syntax in the [unit reference](docs/UNIT-REFERENCE.md). | Directives listed there as experimental. |

A lingering user's services run without the user's credentials: they reach
network shares only with credentials they supply themselves, and DPAPI,
Credential Manager and EFS are unavailable. While linger holds a roaming
profile, the profile stops roaming on that machine. See
[headless user managers](docs/OPERATIONS.md#headless-user-managers).
