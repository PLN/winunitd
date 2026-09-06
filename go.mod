module github.com/PLN/winunitd

go 1.25.0

toolchain go1.27.1

require (
	github.com/Microsoft/go-winio v0.6.2
	golang.org/x/sys v0.47.0
)

replace github.com/Microsoft/go-winio => ./third_party/go-winio
