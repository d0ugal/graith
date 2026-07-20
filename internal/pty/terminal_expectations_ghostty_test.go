//go:build libghostty && cgo && (darwin || linux)

package pty

func selectedBackendAlternateLine() string { return "           in the bo" }
func selectedBackendShrinkLine() string    { return "cann" }
