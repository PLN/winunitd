# Named-pipe listener dependency decision

Status: accepted: carry a pinned patched copy. September 6, 2026.

## Reproduced failure

The pinned `github.com/Microsoft/go-winio v0.6.2` listener can consume its
close-channel request while an outstanding connection completes with another
error. Its close branch translates only nil and ErrFileClosed into the listener
closed sentinel. Other errors return to the accept loop without marking the
listener closed, while Close waits for that loop's done channel indefinitely.

The full manager race suite timed out in watchdog recovery. A focused repeat
reproduced the hang: Close waited for doneCh while listenerRoutine waited in
its top-level select. This differs from an overlapped OS call remaining pending.
The manager retained cleanup ownership; the fake clock did not advance through
its cleanup deadline, so the test cleanup also remained blocked.

The [upstream source](https://github.com/microsoft/go-winio/blob/v0.6.2/pipe.go)
contains this branch. The latest published release checked for this decision is
[v0.6.2](https://github.com/microsoft/go-winio/releases/tag/v0.6.2); current main
also retains the branch. No dependency version was changed in this experiment.

## Initial validation

The [carried patch](dependency-patches/go-winio-v0.6.2-listener-close.patch)
continues to close the pending pipe and drain its connection completion, then
unconditionally reports listener closure after consuming a close request.
It changes only that cancellation branch; ordinary connection errors keep
their existing behavior.

A private copy of the exact pinned module, selected through a temporary Go
modfile, was used for validation. The initial experiment left the normal module cache and repository
requirements unchanged. The accepted implementation below now selects a local replacement. With the patch:

- 100 race-enabled watchdog/restart repetitions pass.
- The full repository race suite and go vet pass.

Separate validation against the original dependency passed twenty repetitions of
four watch ownership regressions and twenty native path-change exercises. The
listener-close hang remains a separate blocker in that original dependency.

These results support the candidate but do not qualify SYSTEM/session behavior
or an installer. The full-suite result uses the patched dependency; it must not
be presented as a passing run against the unmodified pinned module.

## Options

1. **Carry a pinned patched copy (recommended).** Retain the upstream module
   identity and license notices, record the base version and exact patch, build
   through a repository-relative replacement, and make patch review/removal part
   of dependency updates. Add a deterministic close-race regression alongside
   the native stress test. This keeps the established transport, security, and
   connection implementation and creates an explicit maintenance obligation.
2. **Implement our own listener.** Preserve the current pipe security and
   protocol contract, implement native accept/cancellation/handle ownership,
   and qualify these paths on the supported Windows baselines. This avoids
   carrying a dependency patch but introduces substantially more native code
   and a larger qualification obligation. Other go-winio use would still need
   a separate audit.

At the time of the initial experiment, no upstream message had been sent. Both options remain MIT-compatible with winunitd's licensing policy; the maintainer
accepted the patched dependency's maintenance responsibility.

## Accepted implementation and upstream tracking

The repository uses `third_party/go-winio` through a relative Go module
replacement. The upstream v0.6.2 module and MIT notices are retained. A small
helper extraction lets the regression force close consumption before delivering
each connection outcome, without global hooks. Restoring the original branch
makes this regression fail on ERROR_NO_DATA; the patch passes 100 race repetitions.

The same production fix is already proposed in upstream
[PR #369](https://github.com/microsoft/go-winio/pull/369), addressing
[issue #85](https://github.com/microsoft/go-winio/issues/85). We posted
[independent downstream validation](https://github.com/microsoft/go-winio/pull/369#issuecomment-5558649511)
there rather than creating a duplicate. Upstream acceptance is not assumed.
