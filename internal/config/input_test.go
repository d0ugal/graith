package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInputConfigValidation(t *testing.T) {
	tests := map[string]struct {
		mutate  func(*Config)
		wantErr string
	}{
		"global wheel policy": {
			mutate: func(c *Config) {
				c.Input.MouseWheelPolicy = "dreich"
			},
			wantErr: "input.mouse_wheel_policy",
		},
		"agent wheel policy": {
			mutate: func(c *Config) {
				agent := c.Agents["codex"]
				agent.Input.MouseWheelPolicy = "dreich"
				c.Agents["codex"] = agent
			},
			wantErr: "agents.codex.input.mouse_wheel_policy",
		},
		"global gesture name": {
			mutate: func(c *Config) {
				c.Input.Bindings = map[string]string{"dreich_wheel": InputActionScrollMode}
			},
			wantErr: "input.bindings.dreich_wheel: unsupported gesture",
		},
		"agent gesture name": {
			mutate: func(c *Config) {
				agent := c.Agents["codex"]
				agent.Input.Bindings = map[string]string{"dreich_wheel": InputActionScrollMode}
				c.Agents["codex"] = agent
			},
			wantErr: "agents.codex.input.bindings.dreich_wheel: unsupported gesture",
		},
		"global action name": {
			mutate: func(c *Config) {
				c.Input.Bindings = map[string]string{InputGestureMouseWheelUp: "blether"}
			},
			wantErr: `input.bindings.mouse_wheel_up "blether": unsupported action`,
		},
		"agent action name": {
			mutate: func(c *Config) {
				agent := c.Agents["codex"]
				agent.Input.Bindings = map[string]string{InputGestureMouseWheelUp: "blether"}
				c.Agents["codex"] = agent
			},
			wantErr: `agents.codex.input.bindings.mouse_wheel_up "blether": unsupported action`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			test.mutate(cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want error")
			}

			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestEffectiveInputConfigResolvesGlobalAndAgentOverrides(t *testing.T) {
	cfg := Default()

	if got := cfg.EffectiveInput("canny").MouseWheelPolicy; got != InputMouseWheelPolicyOff {
		t.Fatalf("custom agent policy = %q, want %q", got, InputMouseWheelPolicyOff)
	}

	if got := cfg.EffectiveInput("canny").ActionForGesture(InputGestureMouseWheelUp); got != InputActionScrollMode {
		t.Fatalf("custom agent wheel-up action = %q, want %q", got, InputActionScrollMode)
	}

	if got := cfg.EffectiveInput("codex").MouseWheelPolicy; got != InputMouseWheelPolicyRespectTerminalModes {
		t.Fatalf("codex policy = %q, want %q", got, InputMouseWheelPolicyRespectTerminalModes)
	}

	cfg.Input.MouseWheelPolicy = InputMouseWheelPolicyAlways

	custom := Agent{Command: "canny"}
	cfg.Agents["canny"] = custom

	if got := cfg.EffectiveInput("canny").MouseWheelPolicy; got != InputMouseWheelPolicyAlways {
		t.Fatalf("custom inherited policy = %q, want %q", got, InputMouseWheelPolicyAlways)
	}

	custom.Input.MouseWheelPolicy = InputMouseWheelPolicyOff
	custom.Input.Bindings = map[string]string{InputGestureMouseWheelUp: InputActionNone}
	cfg.Agents["canny"] = custom

	effective := cfg.EffectiveInput("canny")
	if effective.MouseWheelPolicy != InputMouseWheelPolicyOff {
		t.Fatalf("custom override policy = %q, want %q", effective.MouseWheelPolicy, InputMouseWheelPolicyOff)
	}

	if got := effective.ActionForGesture(InputGestureMouseWheelUp); got != InputActionNone {
		t.Fatalf("custom override wheel-up action = %q, want %q", got, InputActionNone)
	}
}

func TestLoadMergesPartialAgentInputConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	toml := `
[agents.codex.input.bindings]
mouse_wheel_up = "none"
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	effective := cfg.EffectiveInput("codex")
	if effective.MouseWheelPolicy != InputMouseWheelPolicyRespectTerminalModes {
		t.Fatalf("codex policy = %q, want default %q", effective.MouseWheelPolicy, InputMouseWheelPolicyRespectTerminalModes)
	}

	if got := effective.ActionForGesture(InputGestureMouseWheelUp); got != InputActionNone {
		t.Fatalf("codex wheel-up action = %q, want %q", got, InputActionNone)
	}
}
