//go:build libghostty && cgo && ((darwin && arm64) || (linux && (amd64 || arm64)))

package pty

func selectedBackendAlternateLine() string { return "           in the bo" }
func selectedBackendShrinkLine() string    { return "cann" }
