# Unit semantics qualification

## Native proxy inactivity observation

Background SCM/task observations now deliver exact record/generation/stop-epoch
results through one coordinator handler. Four retained query slots and rotating
admission preserve bounded work; close joins blocked native calls. Only confirmed
stopped/idle states stop BindsTo dependents. Query errors remain unknown and have
bounded diagnostics; fresh native status remains an independent observation.

Regressions cover captured native targets and dependent policies after reload,
delayed results after replacement, blocked-query capacity and close retries,
progress beside a blocked query, error recovery and task-instance disambiguation.
The local Windows task fixture passed three real external-stop/dependent-cleanup
cycles at about one second each. Local SCM fixture creation was skipped for lack
of service-registration access; SYSTEM qualification remains required. This slice
does not close missing-target/paused-state policy or the complete R3 acceptance.

## Unrelated dependency-event allocation

Bound-stop decisions discover active dependents before collecting graph
definitions. With 1024 loaded/retained units whose captured policies bind only
to other peers, the regression reproduced nine allocations per unrelated exit
before this change and now reports zero bytes/allocations. The local benchmark
measured 43.267 microseconds per decision. Twenty race repetitions of the bound
tests and the full uncached Windows race suite/vet passed. Positive plans still
use captured invocation definitions, including retained reload policy, and build
their graph under the decision lock; a reverse dependency index remains a separate
optimization. This does not change the earlier native dependency evidence scope.

## Tracked cooperative stop

September 12, 2026: PR #147 implements one `ExecStop` command with independent
helper ownership, captured invocation context and a forced-cleanup reserve.
The qualified source is `b8f356ed72efb65ed9a4b80b300ab7c782563c22`;
[exact-source CI](https://github.com/PLN/winunitd/actions/runs/34718242627) passed
Windows/Linux tests, vulnerability checks and the Windows cross-build. Merge
`9553fc444db849f0a6641fae0c0fed5a7ea4185e` has the same tested tree.

The disposable guest used the downloaded Windows artifact after manifest, source,
dependency and installed-payload hash verification. Eight real-process cases
passed through an SCM-owned SYSTEM manager and a production headless S4U user
manager:

| Case | SYSTEM | Headless user |
| --- | --- | --- |
| Cooperative helper and clean workload exit | 1.092s | 1.405s |
| Hung helper: force workload, helper and helper child | 8.037s | 8.034s |
| Repeatable oneshot: helper during completion | Passed | Passed |
| Retained oneshot: helper only on explicit stop | Passed | Passed |

The forced cases configured a ten-second stop budget: force followed the
eight-second cooperative allowance, returned failed rather than successful
cooperation, and confirmed cleanup. The checks also verified helper/workload
token identity, working directory, configured environment, `MAINPID` absence for
an exited oneshot, linked invocation identity, helper journal output, and no
re-execution on stop retry. All fixture definitions were removed. Disabling
linger stopped the user manager and desktop helper, released its profile, and
prevented resurrection; the SYSTEM broker remained running.

Required manager regressions in `stop_helper_test.go` additionally cover captured
configuration across reload, restart across successive invocations, failed-start
exclusion, late creation after the caller deadline, failed helper cleanup,
unfinished output retention, parent-budget reservation, the 32-helper admission
limit, and Close joining accepted work. Focused cases passed twenty race-test
repetitions; the full uncached Windows race suite and vet passed.

This closes the single-command R3.3b slice. It does not close the remaining unit
format, dependency, health or multi-session qualification gates, nor qualify MSI
servicing or the real application soak. Multiple stop commands, automatic
console/GUI signals and stop helpers for external proxies remain unsupported.
Raw scripts, process observations and artifact evidence remain in the private
qualification store; private identities and paths are intentionally not copied
into this repository.

## Managed bound-dependent cleanup

September 13: managed peer exit/failure now admits captured BindsTo cleanup,
including reverse stop ordering and recovery suppression before teardown.
One coalescing worker reserves progress outside client stop admission and retains
accepted work across caller/close deadlines. Delayed record/generation/epoch
checks preserve replacement processes and their operation metadata. BindsTo plus
After requires an active peer, including the explicit retained-oneshot case.

Twenty focused race repetitions cover peer recovery without dependent restart,
Requires/PartOf distinction, removed-definition policy, ordered scope suppression,
client stop saturation, stale peer/watchdog/member events, delayed batch metadata,
dependent activation during blocked peer cleanup, retained-peer recovery/start-limit,
and close both before worker dispatch and during blocked cleanup. The full
uncached Windows race suite and vet pass. These are manager regression results;
native artifact qualification and external SCM/task disappearance observation
remain separate. This does not close the full R3.2/R3 milestone.

The immutable Windows artifact at `e0a9ab78466096ea50f96365697201856e3676dd`
subsequently passed ten native dependency cases on the disposable Enterprise LTSC
baseline, split equally between genuine SYSTEM and a headless standard user.
[Exact-source CI](https://github.com/PLN/winunitd/actions/runs/34725134485)
passed before qualification and PR #157 merged the identical runtime tree.
Peer exit stopped the bound dependent while the peer recovered and a Requires-only
dependent stayed active. Reload preserved the running invocation's old binding;
a replacement invocation used the new unbound policy. Both scopes also verified
that repeatable oneshot completion stops its dependent, while retained completion
keeps it active. The peer recovery/observation cases completed in 1.055–1.088 seconds.

The same candidate passed eight cooperative/forced/oneshot stop cases, accepted
user snapshot observation, and pending timer recovery after broker crash in
2.689 seconds with the same activation identity and no replay on a second restart.
Final disable-linger released the manager, helper and user profile and prevented
resurrection. Raw scripts, results, artifact hashes, manifest and CI identity are
retained privately. External SCM/task disappearance and the remaining full R3
acceptance matrix are still open; these results do not qualify MSI servicing or R7.
