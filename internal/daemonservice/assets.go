package daemonservice

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// GenerateAssets writes every static LaunchAgent plist plus the Swift
// controller's generated lookup table from the embedded source manifest.
func GenerateAssets(launchAgentsDir, swiftOutput string) error {
	if err := os.MkdirAll(launchAgentsDir, 0o755); err != nil { // #nosec G301 -- signed app resources must be traversable.
		return fmt.Errorf("create launch agents directory: %w", err)
	}

	definitions := Definitions()
	for _, definition := range definitions {
		path := filepath.Join(launchAgentsDir, definition.PlistName)
		if err := os.WriteFile(path, launchAgentPlist(definition), 0o644); err != nil { // #nosec G306 -- signed plist resources are public.
			return fmt.Errorf("write launch agent %s: %w", definition.Label, err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(swiftOutput), 0o755); err != nil { // #nosec G301 -- generated source output is a build artifact.
		return fmt.Errorf("create Swift output directory: %w", err)
	}

	if err := os.WriteFile(swiftOutput, swiftServices(definitions), 0o644); err != nil { // #nosec G306 -- generated source is not secret.
		return fmt.Errorf("write generated Swift services: %w", err)
	}

	return nil
}

func launchAgentPlist(definition Definition) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>BundleProgram</key>
	<string>Contents/MacOS/gr</string>
	<key>ProgramArguments</key>
	<array>
		<string>gr</string>
		<string>daemon</string>
		<string>start</string>
		<string>--internal-service-label=%s</string>
		<string>--internal-service-slot=%s</string>
	</array>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
	<key>ThrottleInterval</key>
	<integer>1</integer>
</dict>
</plist>
`, definition.Label, definition.Label, definition.Slot))
}

func swiftServices(definitions []Definition) []byte {
	definitions = append([]Definition(nil), definitions...)
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Slot < definitions[j].Slot })

	var out bytes.Buffer
	out.WriteString("// Code generated from internal/daemonservice/service_manifest.json; DO NOT EDIT.\n")
	out.WriteString("import Foundation\n\n")
	out.WriteString("let graithServicePlists: [String: String] = [\n")

	for _, definition := range definitions {
		fmt.Fprintf(&out, "    %q: %q,\n", definition.Slot, definition.PlistName)
	}

	out.WriteString("]\n")

	return out.Bytes()
}
