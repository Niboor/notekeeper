// Package version exposes build information, set at link time.
package version

// Version is overridden with -ldflags "-X notekeeper/core/internal/version.Version=..."
var Version = "dev"
