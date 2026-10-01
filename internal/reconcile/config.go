package reconcile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kcompton15/Todo-Board/internal/store"
)

type Config struct {
	GitLabUser    string   `json:"gitlabUser"`
	JiraEmail     string   `json:"jiraEmail"`
	JiraSite      string   `json:"jiraSite"`
	GitLabGroup   string   `json:"gitlabGroup,omitempty"`
	IssuePrefixes []string `json:"issuePrefixes,omitempty"`
}

var gitlabUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)
var gitlabGroupPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(?:/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`)
var issuePrefixPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]+$`)

const configHint = "run scripts/install-board-reconciler --configure"

var ErrConfigMissing = errors.New("reconciler config not configured; " + configHint)

func DefaultConfigPath(home string) string {
	return filepath.Join(home, ".config", "todo-board", "reconciler.json")
}

func LoadConfig(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read reconciler config: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("invalid reconciler config %s: %w", path, err)
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.GitLabUser == "" || c.JiraEmail == "" || c.JiraSite == "" {
		return ErrConfigMissing
	}
	if !gitlabUsernamePattern.MatchString(c.GitLabUser) {
		return fmt.Errorf("invalid GitLab username %q; %s", c.GitLabUser, configHint)
	}
	at := strings.IndexByte(c.JiraEmail, '@')
	if at < 1 || at == len(c.JiraEmail)-1 || strings.ContainsAny(c.JiraEmail, " \t\r\n") {
		return fmt.Errorf("invalid Jira email %q; %s", c.JiraEmail, configHint)
	}
	if err := store.ValidateHostname(c.JiraSite); err != nil {
		return fmt.Errorf("%w; %s", err, configHint)
	}
	if c.GitLabGroup != "" && !gitlabGroupPattern.MatchString(c.GitLabGroup) {
		return fmt.Errorf("invalid GitLab group %q; %s", c.GitLabGroup, configHint)
	}
	for _, prefix := range c.IssuePrefixes {
		if !issuePrefixPattern.MatchString(prefix) {
			return fmt.Errorf("invalid issue prefix %q; use uppercase Jira project keys; %s", prefix, configHint)
		}
	}
	return nil
}
