package mirror

import (
	"context"
	"os/exec"
)

type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) (string, error)
}

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	return string(output), err
}
