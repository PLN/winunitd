# Hermes pilot qualification (R7)

## October 12–14, 2026

The pilot is the maintainer's dev machine: Windows 11 Enterprise LTSC 2024,
build 26100, one administrator, Hermes running for one local interactive user
without linger or a domain profile. Before R7 a per-user scheduled task started
a 0.2.2-beta user manager, which ran Hermes's gateway, dashboard and WebUI.
After R7 the MSI-installed `winunitd` service runs them. Host names, accounts,
paths, raw logs and the hourly check log are kept privately.

Two unsigned packages were built on the pilot host from clean checkouts of main
with `packaging/wix/build.ps1`. Both commits passed hosted CI.

| Package | Source | Installer version |
| --- | --- | --- |
| 0.1.0-alpha | `4ea24bc` | 0.1.0 |
| 0.1.1-alpha | `b139851` ([#289](https://github.com/PLN/winunitd/pull/289)) | 0.1.1, new ProductCode |

### R7.1 Preflight

On October 12 the maintainer's private runbook recorded the existing scheduled
task and its launcher, the manager process and base directory, the four Hermes
units and their enablement, the owner identity, the three health endpoints with
their authentication, the listening ports and the rollback assets: the exported
task definition and the untouched old base directory.

### R7.2 Handoff

On October 13 at 00:09 CEST the old task was disabled and the old user manager
stopped `hermes.target`. The old manager and its launcher were then ended. The
units and enablement were copied into the installed per-user layout. The
Hermes maintenance unit was left out until it is adapted to the installed CLI.
0.1.0 installed quietly with exit 0 as a LocalSystem service with automatic
start. `winctl verify` accepted the four units, and the user was admitted
explicitly in `user-admission.json`. The broker started the user manager and
the manager started `hermes.target`.

Each port then had one listener, and each listener's process tree descended from
the service-launched user manager. All three health endpoints returned 200, and
the dashboard and WebUI required authentication. The maintainer sent a real chat
through the dashboard. Hermes was down for a few minutes during the handoff.

### R7.3 Exercise

| # | Check | Result |
| --- | --- | --- |
| 1 | Identity and health | Elevated `winctl capabilities` reported the unmodified candidate commit; one owner per port; one real chat |
| 2 | Logoff | The user manager and all three component trees exited; the broker kept running; nothing remained in the old session |
| 3 | Logon | A new user manager started one second after logon; all three components returned once, healthy, within two seconds |
| 4 | Reboot | The service started automatically in session 0; nothing ran user-side before logon; the user manager and all three components started at logon |
| 5 | Kill each component | Gateway, dashboard and WebUI each healthy again in 6.9, 6.3 and 9.4 s under the same manager; the old trees were gone; no duplicate listeners |
| 6 | Kill the user manager | All its workload processes ended with it; a new manager had Hermes healthy 5.6 s later |
| 7 | Kill the user manager three times quickly | Relaunched after 3.2, 9.5 and 9.6 s without a premature backoff reset ([#254](https://github.com/PLN/winunitd/issues/254), [#271](https://github.com/PLN/winunitd/pull/271)); healthy 4.6 s after the last |
| 8 | `winctl --user stop` and `start hermes.target` | Stop returned at once with every workload process gone and the ports closed; start was healthy in 5.5 s |
| 9 | Forced stop | The Hermes units define no `ExecStop`, so stop terminates the job, as row 8 shows |
| 10 | `msiexec /fa` repair | Exit 0; the service restarted and Hermes was healthy 4.2 s later; units, enablement and admission unchanged |
| 11 | Upgrade 0.1.0 to 0.1.1 | Exit 0; only 0.1.1 registered; the service reported `b139851` unmodified; Hermes healthy 3.3 s later; units, enablement and admission unchanged |
| 12 | Rollback and second handoff | Admission `disabled` stopped the user manager within 9 s with the service still running. The re-enabled old task had the previous pilot healthy in 5.4 s. After the second handoff Hermes was healthy 19.9 s after re-admission |

Rows 1 and 5–12 ran on October 13 between 00:10 and 00:26 CEST; rows 2–4 ran at
09:02, 09:04 and 09:12. Unit logs were read with `winctl --user logs` to
attribute each restart.

### R7.4 Soak

The soak ran 24 hours on 0.1.1, from October 13 00:28 to October 14 00:28 CEST,
with no lifecycle fix in between. A read-only check ran every hour and after the
logoff and the reboot, 28 checks in all. Each check covered service state, one
broker, at most one user manager, one listener per port owned by the installed
user manager, health 200, unit states and the disabled old task. Every check
passed.

Component start times changed only at the maintainer's logoff/logon and reboot.
There were no unexpected restarts, no duplicate listeners and no application
outages. The working sets of the service broker and the user manager were 21 and
20 MB at the start and 22 and 21 MB at the end. Private memory, sampled from
07:34 on October 13, stayed between 52 and 54 MB for each process, and handle
counts were steady.

### Limitations

- One machine and one user. R4.1, R4.4 and R4.5 stay open as documented in
  [milestones](MILESTONES.md).
- The graceful-stop escalation after `TimeoutStopSec` is covered by automated
  tests, not by this pilot, because the Hermes units define no `ExecStop`.
- The upgrade row covers 0.1.0 to 0.1.1, which differ only in version identity.
- Long-term memory growth and log accumulation are observed during the wider
  rollout to more machines.
- The packages are unsigned; signing is R8.
