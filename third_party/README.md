# Patched dependencies

`go-winio/` is a complete copy of the verified Go module
`github.com/Microsoft/go-winio v0.6.2`, with its upstream MIT license and
attribution intact. Its module sum is
`h1:F2VQgta7ecxGYO8k3ZZz3RS8fVIXVxONVUPlNERoyfY=`.

Only `pipe.go` is modified; `pipe_close_regression_test.go` is added.
The exact diff and the base file SHA256 inventory are in
`docs/dependency-patches/`. Upstream discussion and the accepted maintenance
tradeoff are in `docs/PIPE-LISTENER-DECISION.md`.

The root module uses a repository-relative replacement. Root `go test ./...`
does not traverse this nested module; Windows CI explicitly runs the added
regressions. The build manifest hashes every file in the replacement tree and
ships its MIT license in `THIRD-PARTY-NOTICES.txt`.

When updating, obtain the new module through Go's checksum verification,
compare the full upstream change, and review upstream PR #369 and issue #85.
Remove the replacement only after an upstream release includes the fix and
passes the regression plus downstream watchdog stress and full qualification.
Otherwise rebase the patch, update the base inventory and exact diff, review
licenses, and repeat these checks. A root go.mod version bump alone does not
update the replacement. Vulnerability tools may lack version matching for local
replacements: review advisories against the pinned base and its transitive
imports as well as running the normal scanner.
