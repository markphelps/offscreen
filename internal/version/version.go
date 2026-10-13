// Package version holds the build version string of the offscreen binaries.
package version

// Build is the release or source version of this binary. Release builds and
// scripts/install.sh set it with
// -ldflags "-X github.com/markphelps/offscreen/internal/version.Build=<version>".
// It is for diagnostics only: compare daemon.ProtocolVersion, never Build,
// to decide what a daemon supports.
var Build = "dev"
