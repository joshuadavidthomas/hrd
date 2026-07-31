// Package version carries the build version stamped into release binaries.
package version

// Version is overridden at release time via -ldflags; source builds report dev.
var Version = "dev"
