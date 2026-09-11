package mirror

import (
	"context"
	"os"
	"os/exec"
)

type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) (string, error)
}

type OSRunner struct {
	GitHubToken string
}

func (r OSRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	args, environment := prepareCommand(name, args, r.GitHubToken)
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func prepareCommand(name string, args []string, token string) ([]string, []string) {
	if name != "git" || token == "" {
		return args, nil
	}
	const helper = `!f() { if [ "$1" = get ]; then echo username=x-access-token; echo "password=$ORG_MIRROR_GITHUB_TOKEN"; fi; }; f`
	prepared := make([]string, 0, len(args)+4)
	prepared = append(prepared, "-c", "credential.helper=", "-c", "credential.helper="+helper)
	prepared = append(prepared, args...)
	return prepared, []string{
		"ORG_MIRROR_GITHUB_TOKEN=" + token,
		"GIT_TERMINAL_PROMPT=0",
	}
}
