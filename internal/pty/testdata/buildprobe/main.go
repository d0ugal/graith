// buildprobe links the PTY package without unrelated CLI dependencies so the
// backend packaging tests can inspect its exact module metadata.
package main

import _ "github.com/d0ugal/graith/internal/pty"

func main() {}
