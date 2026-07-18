//go:build !libghostty || !cgo || (!darwin && !linux)

package pty

const selectedBackendParserPanic = true

func selectedBackendAlternateLine() string { return "in the bothy" }
func selectedBackendShrinkLine() string    { return "keep" }
