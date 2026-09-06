# Builds and dependency maintenance

## Pinned toolchain

Build with Go **1.27.1**, recorded in `.go-version` and the `toolchain` directive. The module's Go **1.25.0** directive is its dependency/language minimum, not the compiler selected for qualification. The fixed Windows syscall dependency requires that minimum. CI reads `.go-version`, disables automatic toolchain switching after setup, and uses full commit pins for checkout, setup-go, and artifact upload.

From the repository root:

```powershell
$env:GOTOOLCHAIN = 'go1.27.1'
go vet ./...
go test -race -parallel 1 ./... -timeout 180s
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
go run ./tools/build
```

Go can download the selected toolchain through its normal verified module mechanism. Alternatively install that version from [official Go downloads](https://go.dev/dl/). The build command rejects a compiler different from `.go-version`. Direct `go test` commands do not enforce exact compiler equality, so record `go version` when reporting local results.

`tools/build` runs on Windows or Linux and defaults to Windows/amd64 output under `dist/`. Use `-goos` and `-goarch` to select another compilation target; compilation alone does not qualify it. The command builds all three executables with CGO disabled and source paths trimmed. It fixes the baseline architecture level and clears ambient build flags/experiments for child builds. Race tests still require the host C compiler; no C compiler is needed for the production binaries.

Use `-version 0.2.0-beta` for a release candidate. The build records that version
in the manifest and links it into the daemon/CLI version output. The beta MSI
entry point is `./packaging/beta/build.ps1 -PackageVersion 0.2.0`; it requires
clean source unless `-AllowDirty` is explicitly supplied for development. It uses
the already approved WiX 7.0.0/.NET 10.0.400 build tooling. Keep `.wixpdb`, SDK
intermediates, and build logs private. Package qualification is still required;
a successful MSI build alone is not a release.

`dist/build-manifest.json` records the compiler, target, source revision, dirty state, module-file hashes, and each binary's SHA256/size. It contains no operator identity, hostname, absolute checkout path, or environment dump. Go's embedded module/build information provides dependency versions and sums (`go version -m`). A failed build leaves no successful manifest for that attempt. Dirty builds are allowed for development and visibly marked; they must not be promoted as release artifacts. Build from a clean checkout and do not modify sources during a qualification build.

CI retains native Windows artifacts and the Linux-to-Windows build manifest for 14 days. These are unsigned CI artifacts, not installation releases or provenance attestations. Compare artifact hashes with the manifest before deploying to a test guest. Pinning the Go compiler and actions does not freeze hosted runner images or the race-test C compiler; record the actual platform when interpreting results.

## Private development CI budget

GitHub's full workflow is now manual (`workflow_dispatch`); pushes, PR updates,
and the weekly schedule do not automatically spend hosted-runner minutes.
Use trusted local Gitea dispatch for reviewed full commit IDs and Proxmox guests
for installation acceptance. Keep deployment configuration and credentials in
the private operator workspace. Local CI must run the same vet, race, nested
pipe regression, maintenance, vulnerability, and build checks; do not equate a
skipped GitHub run with a passing check. Do not admit unreviewed public PR code
to the trusted local runners. GitHub source/release hosting remains independent
of where builds run. Manual hosted verification is reserved for release candidates.

## Maintenance policy

- Run `govulncheck` on Windows and Linux for each admitted CI revision and during weekly dependency review. GitHub hosted verification is manual while routine work uses local CI. Pin the scanner version in the workflow; use the current vulnerability database so newly published advisories are detected.
- Treat reachable vulnerability findings as a failed check. Review package/module-only findings too; do not silently suppress them or describe a reachability result as proof that all dependencies are vulnerability-free.
- Review supported Go patch releases promptly, updating `.go-version` and `go.mod` together. Security updates take priority over routine feature work. Re-run vet, race tests, scanner, builds, and relevant Windows qualification when the compiler or dependencies change.
- Dependabot proposes weekly Go-module and action updates. Review their source/release notes and compatibility; do not automatically merge them. Keep actions pinned to full commits, with readable version comments.
- Review scanner updates and both compiler pins alongside the weekly dependency review; they are not all covered by Dependabot's Go-module support. Keep public artifacts free of local paths and raw identity logs.

## Initial R0.1 evidence

The previous `golang.org/x/sys v0.33.0` imported a package affected by [GO-2026-5024](https://pkg.go.dev/vuln/GO-2026-5024), although the scan found no reachable vulnerable call in this project. Updating to `v0.47.0` removed that finding; the subsequent Windows source scan reported no vulnerabilities.

Local builds with the pinned compiler produced matching hashes for all three binaries across two output directories, including a run with conflicting ambient stripping flags and architecture settings. Manifest hashes matched the actual files. Embedded build metadata confirmed Go 1.27.1, CGO disabled, path trimming, and the baseline amd64 architecture.

Commit `7b788e9` passed Windows vet/race tests, Linux race tests, both vulnerability scans, native Windows builds, and Linux-to-Windows builds in [CI run 33950679809](https://github.com/PLN/winunitd/actions/runs/33950679809). Downloaded artifacts matched their manifests. Comparing hosts exposed Git checkout line-ending differences: Linux cross-build hashes matched the local build, while the hosted Windows checkout used different source bytes.

Commit `40858a8` fixes compiler inputs to LF through `.gitattributes`. All lanes passed again in [CI run 33951099740](https://github.com/PLN/winunitd/actions/runs/33951099740). Downloaded native Windows artifacts matched their manifest, and all three native Windows binary hashes matched the Linux-to-Windows manifest exactly. This proves cross-host reproducibility for those inputs and compiler, not for future changes without rechecking.

Runtime defect reproductions and identity-specific test requirements remain documented in [R0-BASELINE.md](R0-BASELINE.md). The compiler/dependency update does not close those runtime defects or replace installer/session qualification.

## Locally patched pipe dependency

See [third-party maintenance](../third_party/README.md). Build manifest schema 2
adds `third_party_sha256`: SHA256 of the dependency's sorted relative slash paths,
a NUL separator, each file's lowercase SHA256, and a newline per file. It covers
all replacement bytes because go.sum cannot authenticate a local replacement.
The artifacts include the upstream MIT notice for redistribution.

Run the nested-module regressions explicitly on Windows:

```powershell
go -C third_party/go-winio test -race -run 'TestConsumedCloseOverridesConnectionResult|TestConnectionErrorWithoutClosePreserved' -count 100 -timeout 60s .
```
