# Architecture review: winunitd from first principles

Reviewed September 4, 2026 at `b13fa16`. This is an assessment and proposal, not an adopted specification or a claim that the findings have been fixed. Source references are relative to this document. The MSI plan remains a separate implementation roadmap.

September 5 follow-up: the [roadmap](../ROADMAP.md), [Design v2](../DESIGN.md), and [milestones](MILESTONES.md) now adopt this review's direction. The assessment below remains historical evidence; its original design-document references refer to [revision 1](archive/DESIGN-v1.md). Adoption does not fix the reported runtime defects.

The concept is sound. I would build a Windows-native application supervisor with familiar systemd concepts, and keep much of this foundation. I would start with substantially less functionality and make process ownership, lifecycle transitions, shutdown, and Windows identity the first deliverables. I would keep Go today; a language rewrite would address little of the risk found here.

## 1. What product is worth building?

For Hermes, the useful product is one place to describe, start, stop, recover, and inspect a cooperating group of background processes. The gateway, dashboard, and web interface are suitable units when they are independent processes. Components inside one process need application instrumentation; the supervisor cannot independently restart an in-process component merely by adding a unit.

The differentiator should be coordinated lifecycle, user execution, and understandable failure reporting. Running an executable as a service alone is already addressed by projects such as [WinSW](https://github.com/winsw/winsw). If that were the entire requirement, I would use an existing wrapper. Windows itself remains responsible for boot, sessions, security identities, and native services. winunitd should orchestrate applications within that environment.

One SCM service owning ordinary processes is appropriate for a coordinated application supervisor. It gives a coherent graph and avoids registering every short-lived command as a service. The cost is a shared failure domain: a manager failure or upgrade can interrupt many workloads. This is acceptable for the initial target if explicitly documented and tested. I would later consider separate manager instances for unrelated critical applications only if users need fault isolation.

External SCM services and scheduled tasks should be explicit adapters. Windows retains their ownership and recovery policy. Their capabilities and status should identify that they are externally managed. The current proxy types correctly avoid rewriting their native definitions, but their lack of continuous exit monitoring means they cannot make the same supervision promises as owned processes.

## 2. Decisions I would keep

- **Job Objects for owned process trees.** Creating processes suspended, assigning the job, and then resuming is the right sequence. Explicit inherited-handle lists are valuable. Keep kill-on-close as the default crash policy rather than attempting to rediscover ownership from PIDs after restart.
- **A portable logic core over Windows adapters.** The split between `core`, `manager`, `runtime`, `unit`, `timers`, `journal`, and `protocol` is useful. Fake clocks and injected launchers make difficult cases testable without pretending Linux tests exercise Windows security.
- **Separate dependency and ordering relationships.** `Wants`/`Requires` answer whether something is pulled into an operation; `After`/`Before` order participating operations. Preserve this distinction.
- **Explicit machine/user scopes and local named pipes.** DACLs, client-token checks, server-owner verification, and rejection of remote clients suit a local administrative tool. There is no need for an HTTP control server in the initial product.
- **Declarative configuration and explicit enablement.** Enabling should remain separate from starting. Small enable records avoid requiring symlink privileges. Unknown directives should remain errors so copied Linux settings do not silently appear effective.
- **Small dependencies and simple deployment.** Go modules, `go-winio`, `x/sys/windows`, and three binaries are a reasonable starting point. JSON-line workload logs with rotation and invocation IDs are adequate for the pilot.

Job membership is process containment, not a security sandbox. Windows documents exceptions such as children launched through `Win32_Process.Create`; brokers and external services can also do work outside the direct process tree. Promise ownership of processes actually contained in the job, not containment of every action a workload might cause. See [Microsoft Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects).

## 3. The most important architectural problem: lifecycle authority

The code has an explicit state machine, but it does not have exclusive authority over state. Graph transactions produce their own state maps; `applyRunLocked` copies outcomes onto runtime records; process watchers and restart/watchdog paths also change those records. Generation checks, per-unit operation locks, and special cases reconcile the results. See [manager.go](../internal/manager/manager.go), [supervise.go](../internal/manager/supervise.go), and [unitruntime.go](../internal/manager/unitruntime.go).

Those protections are useful, but the number of paths that need them is the underlying concern. A Go race detector cannot detect a logically stale result written while correctly holding a mutex.

From the start I would use one serialized lifecycle coordinator per manager. It would own immutable configuration revisions, unit runtime records, and operation ordering. Blocking Windows calls, process waits, probes, and log draining would run in workers and return events tagged with an operation generation. The coordinator would discard stale completions. Independent processes would still launch concurrently; serialization of decisions does not require serializing I/O.

Keep four distinct concepts: loaded configuration, requested lifecycle operation, observed process state, and application health. A transaction should submit and observe operations, not later overwrite current runtime state with a batch of conclusions. Startup graphs do not automatically imply that every requested unit must remain active forever: manual stops, oneshots, and restart policy still govern behavior.

For a small supervisor, an event loop plus bounded workers is enough. I would not introduce a distributed reconciler, plugin framework, or elaborate actor library.

## 4. Findings that should precede a public installer

### High: reload can lose control of a live workload — reproduced

[reload.go](../internal/manager/reload.go) builds the new runtime map only from successfully parsed files. Missing or invalid files disappear from that map. Their asynchronous supervision is detached, while the process is deliberately left running.

A temporary Windows test started a sleeping process in an isolated manager, deleted its unit file, and reloaded. The process remained alive; both `Status` and `Stop` returned `not-found`. This also makes it possible to lose ordinary control of a workload after a configuration typo. A recreated unit can acquire a new runtime record while the previous invocation remains alive.

Keep active runtime records independently of the configuration index. Represent missing/invalid configuration as load state, retain the active invocation's configuration, and preserve status and stop until that invocation terminates. Prefer rejecting an invalid replacement or retaining the last valid revision with explicit diagnostics. Reload must not erase ownership.

### High: oneshot startup can deadlock on output — reproduced

[exec_windows.go](../internal/runtime/exec_windows.go) waits for a oneshot to exit before returning its process. [supervise.go](../internal/manager/supervise.go) attaches stdout/stderr capture only after that return. A child that fills the pipe cannot finish, while its supervisor is waiting for it to finish before reading.

In an isolated temporary manager, a PowerShell oneshot writing 10 characters succeeded in about 0.55 seconds. The same command writing 200,000 characters hit its three-second startup timeout. The child was stopped by the test's timeout path.

Separate process creation from activation completion. Return the process and attach output readers promptly; then wait for oneshot completion or notify readiness at the manager layer. Log draining must not depend on successful activation.

### High: stopping is not yet a trustworthy lifecycle contract — source inspection

[shutdown.go](../internal/manager/shutdown.go) discards the context in `stopUnitCtx` and ignores the owned process stop error. [exec_windows.go](../internal/runtime/exec_windows.go) immediately kills the job, ignores its wait result, and closes the process. Increasing `TimeoutStopSec` therefore does not give the application a graceful shutdown period.

Define an application-specific stop operation first, optional console control where its Windows console arrangement supports it, and forced job termination after a bounded deadline. Preserve errors and unknown/still-stopping state until termination is established. All stop paths should share an aggregate deadline. This belongs in the runtime before installer rollback depends on it. Do not promise CTRL_BREAK merely because a new process group was requested; the current launcher also uses `CREATE_NO_WINDOW`.

### High: user-manager lifecycle and Windows launch context need qualification — source inspection

[userhost.go](../internal/manager/userhost.go) starts managers on logon/reconciliation/linger operations, but has no continuous process-exit recovery loop. Its close path kills managers, and its launch path needs cancellation/generation protection against concurrent shutdown and logoff.

There is also a concrete Windows API contract to resolve: [usermgr_windows.go](../internal/runtime/usermgr_windows.go) sets inherited standard handles and passes `bInheritHandles=true` to `CreateProcessAsUser`. Microsoft documents that handle inheritance cannot cross sessions. A LocalSystem service in session 0 launching an interactive WTS token is exactly the case that needs a real SYSTEM-to-user test and a launch arrangement that obeys that restriction. This review did not reproduce the failure under SYSTEM. See [CreateProcessAsUserW](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-createprocessasuserw).

### Medium: persistent timer storage is weaker than the product promise — source inspection

[state.go](../internal/timers/state.go) overwrites timer state directly and treats read/parse errors as empty state. [engine.go](../internal/timers/engine.go) ignores save failures. A corrupt or unwritable file can silently alter catch-up behavior.

Use atomic replacement, report persistence failures, and distinguish missing from corrupt state. Document whether missed executions are coalesced, retried, or skipped. Exactly-once application side effects cannot be guaranteed by the scheduler's state file; scheduled commands must tolerate the chosen delivery semantics. SQLite is optional, not a prerequisite for fixing this.

### Medium: log capture needs explicit resource bounds — source inspection

[store.go](../internal/journal/store.go) reads output with `ReadString('\n')`. File rotation limits stored file size, but does not bound an unfinished line in memory. A continuously writing child without newlines can therefore consume manager memory. Bound or chunk records, choose a policy for slow/full storage, and report dropped output. Keep daemon lifecycle diagnostics separate from workload output. In particular, stderr is a stream, not reliable proof of error severity.

## 5. Copy systemd concepts with an explicit compatibility contract

Familiar names help only if an operator can trust them. Windows-native extensions are reasonable; changing portable semantics under existing names is harder to justify.

| Concept | Current behavior / concern | What I would choose |
| --- | --- | --- |
| `Requires` versus `After` | Appropriately separated | Keep; test startup, explicit stop, unexpected exit, and restart separately |
| `BindsTo` | Included in start dependencies and explicit stop propagation; process-exit watcher does not propagate the bound unit's disappearance | Implement continuous binding semantics or reject the directive until supported |
| `PartOf` | Reverse stop propagation exists; `Restart` is stop then start of the requested root | Restart the affected participating units too; a reverse member not pulled in by the root can currently remain stopped |
| `Type=oneshot` | Successful exit can leave `active` with no process; no `RemainAfterExit` directive | Default to completed/inactive, with an explicit retained-active option for initialization units |
| `PathExists` | Repeated paths mean AND | Preserve familiar OR, or use an explicit different name/operator for AND |
| `CPUQuota` / `CPUWeight` | Machine-wide percent and coarse Windows job weights | Expose Windows units/ranges clearly; prefer distinct names over misleading apparent equivalence |
| `Type=scm` / `scheduled-task` | Existing Windows objects, no owned job and no continuous supervision | Keep as explicitly limited adapters |
| `.registry` / `.eventlog` | Sensible native triggers | Defer further expansion until process lifecycle is dependable |

The comparison is against upstream [unit semantics](https://raw.githubusercontent.com/systemd/systemd/main/man/systemd.unit.xml), [service semantics](https://raw.githubusercontent.com/systemd/systemd/main/man/systemd.service.xml), and [path semantics](https://raw.githubusercontent.com/systemd/systemd/main/man/systemd.path.xml). The mismatches above are source-inspection findings, not all live reproductions. A target's `Wants` can mask the `PartOf` restart issue by pulling members back in; that does not implement the general relationship.

I would publish a small compatibility table with supported, deliberately different, and rejected directives. Windows does not require an AND interpretation for `PathExists`, or incomplete `PartOf`; these are product choices that can still be corrected before stable releases.

Keep the INI format. I would make explicit executable-plus-arguments the canonical form and treat the current command-string parser as a documented convenience. Windows argument quoting, JSON escaping, and shell syntax must not be conflated. Launch PowerShell or cmd explicitly when their semantics are wanted. Absolute executable paths and literal environment values are good defaults.

## 6. Identity, sessions, and health should be explicit

One background manager per SID is reasonable. A user's SID, logon token, desktop session, loaded profile, and access to remote credentials are different properties, however. Starting headlessly at boot with S4U cannot be assumed equivalent to starting from the user's interactive session.

[userenv.go](../internal/runtime/userenv.go) currently copies the parent environment, removes selected variables, and synthesizes profile folders. I would obtain the target user's Windows environment/profile information, define overrides, and allowlist any inherited broker variables. AppData redirection and arbitrary LocalSystem environment values should not be handled by string substitution. Microsoft explicitly distinguishes creating a process, loading a profile, and supplying its environment in the [process creation documentation](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-createprocessasuserw).

I would initially support headless background workloads and clearly distinguish interactive-token operation from boot-time linger. Desktop/GUI workloads should eventually use a session-specific executor with explicit policy rather than being inferred from one SID-wide manager. Linger is valuable, but it is an advanced Windows identity feature, not just an enable flag.

For Hermes, readiness should mean the component is usable. A created process or open TCP port is weaker evidence than an application readiness endpoint. Readiness should gate dependent startup; liveness should decide whether recovery is warranted. Probe failures should have configurable thresholds and startup grace, and routine upstream/network failures should not automatically imply a restart. The current HTTP watchdog fails on a single failed probe; I would make that an explicit policy rather than the only behavior.

Keep LocalSystem operations narrowly scoped. Separate user execution is already valuable. Before adding arbitrary service-account support, define the threat boundaries and verify that writable user configuration never becomes privileged input. A full broker/worker process split could reduce privileged code later, but is not necessary to rewrite the existing project immediately.

## 7. Language and tooling choices

I would likely choose **Go for this project's current scale**, even starting over. Its straightforward deployment, standard library, concurrency primitives, and test tooling suit an I/O-heavy supervisor. The current defects concern lifecycle contracts and Windows semantics; garbage collection is not the demonstrated constraint. Keep explicit handle ownership and thread-affine token operations inside small Windows adapters.

Rust would be a strong alternative for a team already comfortable with it, especially for handle ownership and invalid-state prevention. It still requires careful unsafe Windows interop and does not automatically fix scheduling, identity, or stale-operation semantics. I disagree with the design document's unconditional suggestion that Rust is the strongest choice regardless of team and scope.

C# is also credible for a Windows-focused team. Advanced Job Object/token operations still require interop. A preinstalled runtime is not mandatory for every deployment: self-contained and [Native AOT](https://learn.microsoft.com/en-us/dotnet/core/deploying/native-aot/) are options, subject to feature constraints. I would not change languages now or add a second runtime merely to access a few APIs.

Keep WiX/MSI for machine-wide service installation, with the existing plan's transactional ownership and VM tests. Signing establishes provenance/publisher trust, not runtime correctness. Installer development can proceed alongside stabilization, but publishing a signed MSI must not become a substitute for qualification.

Improve the build process incrementally:

- Separate the module's minimum language version from the maintained release compiler. CI currently selects from `go 1.24`; local review used Go 1.27.0. Go 1.24 is outside the current support window. Pin a supported patched release toolchain and record it in artifacts. See [Go release policy](https://go.dev/doc/devel/release).
- Pin release actions to reviewed commit SHAs, with automated update proposals. Keep module checksums and add dependency vulnerability checking; avoid a large generic lint configuration without demonstrated value.
- Keep Linux logic tests and Windows race tests. Add parser/protocol fuzzing and operation-sequence tests for start/stop/reload/exit races. Test invariants, not just expected branches.
- Add a disposable Windows VM lane for real SCM startup, non-admin users, cross-session token launches, reboot/logoff, disk-full behavior, and MSI rollback. Hosted administrator tests cannot substitute for these contexts.
- Replace the long mixed-status design document with a short architecture overview, a tested behavior reference, and small decision records. Proposed `Type=exec`, hooks, and other examples should not resemble shipped functionality.

## 8. How I would build it, and how to proceed now

Starting from zero, I would deliver these vertical slices:

1. A console user supervisor for one foreground process: exact arguments/environment, Job Object ownership, immediate output draining, readiness, graceful/forced stop, and bounded restart policy.
2. A small coordinator supporting multiple services and targets, truthful status, and safe configuration reload. Prove all operation interleavings before broadening unit types.
3. SCM hosting and a real per-user launch path, tested from session 0 under LocalSystem. Qualify identity, profile, environment, crash recovery, and logoff.
4. Timers with specified crash/catch-up semantics, followed by MSI servicing and the Hermes migration.
5. Additional triggers, external adapters, resource controls, and linger modes as actual workloads require them.

For the existing repository, do not throw away implemented features or restart development. Freeze expansion temporarily. Fix reload ownership, output-before-wait, and stop/error semantics first; capture them as permanent regression tests during implementation. Then make lifecycle state authoritative, close the user-manager launch/recovery gaps, and settle the systemd compatibility table before a stable release. Reuse the working adapters, parser, graph planner, and fake-clock tests throughout.

## Review validation and limits

Read the design, README, installer plan, CI, and representative implementations across graph execution, lifecycle, reload, process launch, identity, IPC, timers, and logs. This was an architecture review, not an exhaustive security audit.

`go test -race -parallel 1 ./internal/core ./internal/unit ./internal/timers ./internal/journal` passed. Two temporary, isolated Windows manager tests reproduced the live-unit reload loss and oneshot output timeout described above. The temporary test source was removed after recording results; no runtime fix is included in this review. Their own child processes were stopped, and no Hermes interruption was needed. No new SYSTEM, reboot, installer, or full-suite qualification is claimed.
