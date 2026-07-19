package cli

import (
	"fmt"
	"os"

	"github.com/d0ugal/graith/internal/protocol"
)

func warnUnsandboxedStart(info protocol.SessionInfo) {
	warning := unsandboxedStartWarning(info)
	if warning == "" {
		return
	}

	fmt.Fprintln(os.Stderr, warning)
}

func unsandboxedStartWarning(info protocol.SessionInfo) string {
	if info.Sandboxed {
		return ""
	}

	return fmt.Sprintf(
		"warning: session %s started without Graith's sandbox; Graith is not enforcing OS isolation (an external sandbox or VM may still apply)",
		info.Name)
}
