# Windows identity and manager qualification

September 13, 2026: R2/R3 dependencies are satisfied. R4.3 SCM/maintenance
technical acceptance is complete. The broader R4 launch/session/security/linger
matrix remains open. This ledger distinguishes implemented behavior and accepted
native observations from the remaining qualification; older checkpoint wording
in other evidence documents describes status at the time of those runs.

## SCM readiness and combined maintenance acceptance

The [maintenance contract](OPERATIONS.md#global-maintenance) closes admission
across system and user managers, retains one deadline per accepted attempt and
keeps unfinished cleanup owned. Both listeners must open before SCM readiness;
invalid workload configuration leaves control available for diagnosis and repair.

Two disposable Windows Enterprise LTSC build 26100 qualifications exercised the
real SCM service and an admitted interactive standard user. Exact-source CI
passed, and the merged trees match their tested sources:

| Source / PR | CI | SCM ready | Stop during pending notify boot | Combined maintenance | Equal-tree merge |
| --- | --- | --- | --- | --- | --- |
| `5443e4285a7fed74c7633a5a6224308ce50b78d1`, [#128](https://github.com/PLN/winunitd/pull/128) | [34704903915](https://github.com/PLN/winunitd/actions/runs/34704903915) | 0.281s | 0.068s | 0.049s | `a81abe7fd28ad505eb772b3964ca0da458c0f85f` |
| `d173bf4c0893fa1cb679c8915efe808ca170d5df`, [#136](https://github.com/PLN/winunitd/pull/136) | [34709244930](https://github.com/PLN/winunitd/actions/runs/34709244930) | 0.278s | 0.009s | 0.111s | `02625d89fbb07c15f633926c2e256da3dd495b60` |

Both runs verified invalid cold-load repair, system/user quiescence, admission
remaining closed, idempotent maintenance and enabled-workload recovery after SCM
restart. The later run filled all 128 ordinary control connections before using
the independently reserved maintenance pipe. Standard-user control/environment,
manager and broker crash recovery, final logoff/profile release and absence of
resurrection passed. Temporary session-start configuration was restored and the
live pilot was unchanged. Exact artifacts, scripts, results and cleanup evidence
are retained privately; the source and CI identities above were rechecked for
this acceptance ledger.

Subsequent [control saturation qualification](R2-EVIDENCE.md#control-overload-and-reserved-maintenance-acceptance)
at `90a2689` ran ten times per SYSTEM/headless-user identity against an actual
Windows workload, confirming quiescence through the reserved endpoint while
ordinary connections remained occupied. That later fixture uses loopback
transport; it complements the real named-pipe/SCM run above. The completed
[coordinator audit](R2-COORDINATOR-AUDIT.md) covers admission, deadline ownership,
late native completion, reserved capacity and bounded independent shutdown.

This delivers R4.3. It does not close the broader R4 session/security matrix,
the full MSI servicing contract, or the R7 migration/soak gate.

## Current implementation and remaining identity matrix

| Work package | Delivered behavior and evidence | Remaining qualification |
| --- | --- | --- |
| R4.1 Launch context | Interactive `CreateProcessAsUser` launch disables handle inheritance, obtains the target environment/known folders and retains a Windows-owned profile handle. Suspended creation and job ownership precede execution. Earlier genuine SYSTEM-to-interactive launch/crash/logoff observations are recorded in the [admission contract](USER-ADMISSION.md#implementation-evidence-and-limits). | Complete environment/profile and session matrix, including unsupported/redirected/unavailable profiles and cross-session security boundaries. |
| R4.2 User host | Protected explicit/delegated admission, per-SID overrides, session reconciliation, fresh-token recovery, bounded backoff and cancellation are implemented. [Policy/admission qualification](R2-EVIDENCE.md#user-policy-and-cleanup-admission-acceptance) and [real S4U snapshot/recovery](R2-EVIDENCE.md#system-user-host-snapshot-qualification) cover their stated scopes. Dedicated admission administration commands and installer controls remain separate work; protected file administration is available. | Multiple sessions for one SID, distinct concurrent users, rapid real transitions, policy changes during native launch and shutdown, and consolidated no-resurrection evidence. |
| R4.4 Security | Protected policy and pipe checks, bounded impersonation workers and reparse-point rejection are implemented. [Pipe failure handling](R2-EVIDENCE.md#september-12-pipe-impersonation-failure-handling) and native launch regressions cover selected paths. | Actual cross-user rejection, filtered-token cases, privileged paths/reparse attacks and inherited-handle checks under the required identities. Same-caller or delayed-adapter tests alone do not establish those boundaries. |
| R4.5 Linger | Headless local-account S4U launch uses `CreateProcessWithTokenW(LOGON_WITH_PROFILE)` and an exclusive private desktop helper. Windows owns profile lifetime; the old manual `LoadUserProfile` path is removed. [Production crash/recovery](R3-EVIDENCE.md#managed-bound-dependent-cleanup) at `e0a9ab7` and [two actual user-manager replacements](R2-EVIDENCE.md#system-user-host-snapshot-qualification) at `724cab6` verify profile/process cleanup in their scenarios. | Complete explicit mode/profile/session matrix and credential limitations. These observations do not qualify network authentication or domain/cloud/roaming profiles. Unqualified credential-store modes remain outside supported claims. |

The R4 exit gate still requires the complete real-identity matrix. Hosted
administrator tests, simulated session events and consumer evidence cannot
substitute for missing disposable SYSTEM/session observations.
