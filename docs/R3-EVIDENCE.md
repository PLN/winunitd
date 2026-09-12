# Unit semantics qualification

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
