// Package version exposes build information injected by the release pipeline.
package version

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// String returns the public release version.
func String() string { return Version }
