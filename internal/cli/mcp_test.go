package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/d0ugal/graith/internal/protocol"
)

func TestRenderMCPList(t *testing.T) {
	var buf bytes.Buffer

	renderMCPList(&buf, nil)

	if !strings.Contains(buf.String(), "No MCP servers configured") {
		t.Errorf("empty list: %q", buf.String())
	}

	buf.Reset()
	renderMCPList(&buf, []protocol.MCPServerStatus{
		{
			Name:      "braw",
			Sandboxed: true,
			Connections: []protocol.MCPConnectionInfo{
				{ProxyID: "sess1-braw", PID: 42, Running: true, Uptime: "1m0s", UptimeSec: 60},
				{ProxyID: "sess2-braw", PID: 43, Running: true, Uptime: "5m0s", UptimeSec: 300},
			},
		},
		{Name: "canny", Sandboxed: false},
		{Name: "graith", Sandboxed: false, AutoInjected: true},
	})

	out := buf.String()

	for _, want := range []string{"NAME", "SANDBOX", "braw", "on", "canny", "off", "graith", "auto", "config"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}

	// braw has two connections; the longest uptime (5m0s) should be reported.
	if !strings.Contains(out, "5m0s") {
		t.Errorf("expected longest uptime 5m0s in output:\n%s", out)
	}
}

func TestRenderMCPLogs(t *testing.T) {
	var buf bytes.Buffer

	renderMCPLogs(&buf, protocol.MCPLogsResponse{Name: "dreich"})

	if !strings.Contains(buf.String(), "No captured logs") {
		t.Errorf("empty logs: %q", buf.String())
	}

	// Single file: no header, just content.
	buf.Reset()
	renderMCPLogs(&buf, protocol.MCPLogsResponse{
		Name: "ken",
		Files: []protocol.MCPLogFile{
			{ProxyID: "sess1-ken", Content: "speir one\n"},
		},
	})

	single := buf.String()
	if strings.Contains(single, "==>") {
		t.Errorf("single file should have no header: %q", single)
	}

	if !strings.Contains(single, "speir one") {
		t.Errorf("single file content missing: %q", single)
	}

	// Multiple files: each gets a proxy-ID header.
	buf.Reset()
	renderMCPLogs(&buf, protocol.MCPLogsResponse{
		Name: "ken",
		Files: []protocol.MCPLogFile{
			{ProxyID: "sess1-ken", Content: "speir one\n"},
			{ProxyID: "sess2-ken", Content: "speir two\n"},
		},
	})

	multi := buf.String()
	for _, want := range []string{"==> sess1-ken <==", "==> sess2-ken <==", "speir one", "speir two"} {
		if !strings.Contains(multi, want) {
			t.Errorf("multi output missing %q:\n%s", want, multi)
		}
	}
}
