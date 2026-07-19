//go:build !libghostty || !cgo || (!darwin && !linux)

package pty

func selectedBackendAlternateLine() string { return "in the bothy" }
func selectedBackendShrinkLine() string    { return "keep" }
