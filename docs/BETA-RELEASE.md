# Public beta delivery plan

September 6, 2026. This is the immediate release gate, approved by the maintainer.
It takes precedence over the full R0-R8 sequence for the first public beta.
The architecture milestones remain a development backlog, not prerequisites
for shipping every feature already usable today.

## Scope

Ship a usable Windows 11 Enterprise/LTSC x64 process supervisor with a documented,
mostly stable unit format. The initial supported path is the system service
managing simple services and targets, including logging, reload, restart policy,
and ordered dependencies. Notify and interactive user operation may be included
once their focused installation checks pass. Other existing capabilities remain
available with explicit experimental status; their complete qualification does
not block the core beta.

Preserve the current unversioned unit format during the beta series. Do not
rename directives or silently change argv, completed oneshot, PathExists, or CPU
semantics to match the target design. Future incompatible semantics require an
explicit version/migration boundary. Additive directives remain possible.

## Delivery checklist

- [x] **B1 Syntax contract:** publish the implemented unit reference, runnable
  examples, and compatibility regressions for argv, defaults, list handling,
  and unknown directives. Document experimental and unsupported behavior.
  [Reference](UNIT-REFERENCE.md) and [worker examples](../examples/beta/README.md)
  are in-tree. Existing parser/argv/default/list/unknown-directive regressions,
  shipped-example parsing, the full local race suite, and vet pass. JSON null
  arguments now fail verification instead of silently becoming empty strings.
- [x] **B2 Installable candidate:** produce a versioned MSI containing the three
  binaries, licenses, and usage instructions. Test fresh install, ordinary repair,
  upgrade, uninstall with retained data, and a failed upgrade/recovery path in
  disposable Enterprise/LTSC guests. Do not replace files while ownership is
  unresolved. Manual pre-stop is acceptable for this beta if enforced and documented.
- [x] **B3 Useful deployment:** verify start/stop/restart, logs, configuration
  reload, workload crash recovery, daemon restart, and reboot using the packaged
  build. Exercise the Hermes maintenance path. Record limitations and a recovery
  procedure; an exhaustive identity/failure matrix and seven-day soak are deferred.
- [ ] **B4 Publish:** finish the targeted privacy and hosted-artifact review,
  prepare beta release notes and checksums, and verify the downloaded package.
  Public visibility and publishing remain explicit release actions. Signing
  provider enrollment proceeds separately; a clearly identified unsigned beta
  is acceptable if signing would otherwise hold up usable delivery.

The product MSI now passes its complete two-phase lifecycle fixture on Windows
11 Enterprise LTSC build 26100.9168. The same packaged binaries also pass the
existing interactive Hermes pilot's maintenance rehearsal and agent response
check. See [beta qualification](BETA-QUALIFICATION.md) for exact source, package
hashes, evidence boundaries, and the CI test-fixture correction. Hosted artifact
review and the explicit publication action remain under B4.

## Release blockers and follow-up

Block on reproducible ownership loss, unsafe installation/replacement, unusable
basic commands, or ambiguity in the documented core syntax. Fix defects found in
the supported path and retain useful regression tests.

Do not block solely on completing the coordinator refactor, immutable aggregate
status, every operation deadline, advanced identity/linger modes, comprehensive
timer durability, exhaustive stress qualification, SignPath enrollment, or the
full R7 soak. Keep limitations visible and avoid supporting scenarios whose known
failures would make the beta misleading. Stop currently terminates the owned job;
applications requiring a graceful shutdown hook are outside the core beta scope.

Next work is B4. Revisit broader architecture only when it fixes a concrete beta
blocker or after the beta ships.
