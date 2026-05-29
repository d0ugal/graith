package daemon

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/d0ugal/graith/internal/config"
)

func validateModel(agent config.Agent, model string) error {
	if model == "" || agent.ValidateModel == "" {
		return nil
	}

	parts := strings.Fields(agent.ValidateModel)
	if len(parts) == 0 {
		return nil
	}
	bin, lookErr := exec.LookPath(parts[0])
	if lookErr != nil {
		return fmt.Errorf("validate model: cannot resolve %q: %w", parts[0], lookErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, parts[1:]...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := fmt.Sprintf("validate model: %s failed: %v", bin, err)
		if s := strings.TrimSpace(stderr.String()); s != "" {
			msg += "\n" + s
		}
		return fmt.Errorf("%s", msg)
	}

	var valid []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		before, _, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		if id := strings.TrimSpace(before); id != "" {
			valid = append(valid, id)
		}
	}

	if len(valid) == 0 {
		return fmt.Errorf("validate model: %s produced no recognized models", bin)
	}

	for _, v := range valid {
		if v == model {
			return nil
		}
	}

	return fmt.Errorf("invalid model %q; valid models:\n  %s", model, strings.Join(valid, "\n  "))
}
