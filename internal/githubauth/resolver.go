package githubauth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

type Config interface {
	Get(keys []string) (string, error)
}

type Resolver struct {
	Config     Config
	LookupEnv  func(string) (string, bool)
	KeyringGet func(service, account string) (string, error)
}

func (r Resolver) Token(host string) (string, string, error) {
	for _, name := range tokenEnvironmentVariables(host) {
		if token, ok := r.LookupEnv(name); ok && strings.TrimSpace(token) != "" {
			return strings.TrimSpace(token), "", nil
		}
	}

	user, _ := r.Config.Get([]string{"hosts", host, "user"})
	if user != "" {
		if token, err := r.Config.Get([]string{"hosts", host, "users", user, "oauth_token"}); err == nil && token != "" {
			return token, user, nil
		}
	}
	if token, err := r.Config.Get([]string{"hosts", host, "oauth_token"}); err == nil && token != "" {
		return token, user, nil
	}
	if user == "" {
		return "", "", fmt.Errorf("no active GitHub CLI account configured for %s; run gh auth login", host)
	}

	value, userErr := r.KeyringGet("gh:"+host, user)
	if userErr != nil {
		value, userErr = r.KeyringGet("gh:"+host, "")
	}
	if userErr != nil {
		return "", user, fmt.Errorf("read GitHub CLI credentials for %s: %w", user, userErr)
	}
	token, err := decodeKeyringValue(value)
	if err != nil {
		return "", user, err
	}
	if token == "" {
		return "", user, errors.New("GitHub CLI credential is empty; run gh auth login")
	}
	return token, user, nil
}

func tokenEnvironmentVariables(host string) []string {
	if host == "github.com" {
		return []string{"GH_TOKEN", "GITHUB_TOKEN"}
	}
	return []string{"GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}
}

func decodeKeyringValue(value string) (string, error) {
	const prefix = "go-keyring-base64:"
	if !strings.HasPrefix(value, prefix) {
		return value, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		return "", fmt.Errorf("decode GitHub CLI keyring credential: %w", err)
	}
	return string(decoded), nil
}
