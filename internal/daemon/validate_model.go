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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, parts[0], parts[1:]...).Output()
	if err != nil {
		return fmt.Errorf("validate model: failed to run %q: %w", agent.ValidateModel, err)
	}

	var valid []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			valid = append(valid, line)
		}
	}

	for _, v := range valid {
		if v == model {
			return nil
		}
	}

	return fmt.Errorf("invalid model %q; valid models:\n  %s", model, strings.Join(valid, "\n  "))
}
