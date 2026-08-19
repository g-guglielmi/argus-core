// Package buildinfo carries build-time metadata stamped into the binary via -ldflags.
package buildinfo

// Version is the running Argus version, injected at build time with
//
//	-ldflags "-X argus/internal/buildinfo.Version=<git describe>"
//
// It is a `git describe --tags` string: a clean release tag like "v0.4.10", a
// development build ahead of the last release like "v0.4.10-3-gabc1234", or a bare
// short SHA when no tag is reachable. Empty in un-stamped local `go build` runs.
var Version = ""
