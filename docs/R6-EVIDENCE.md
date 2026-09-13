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
not complete R6: production maintenance integration, migration tooling and the
full platform/locked-file/rollback acceptance matrix remain. R7 handoff and its
seven-day soak have not started.
