//go:build !libghostty || !cgo || (!linux && (!darwin || !arm64))

package pty

func selectedBackendAlternateLine() string { return "in the bothy" }
func selectedBackendShrinkLine() string    { return "keep" }
