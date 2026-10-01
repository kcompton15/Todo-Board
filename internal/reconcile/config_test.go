package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kcompton15/Todo-Board/internal/store"
)

func TestMain(m *testing.M) {
	if err := store.SetJiraSite("example.atlassian.net"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func validConfig() Config {
	return Config{GitLabUser: "reviewer.one", JiraEmail: "owner@example.com", JiraSite: "example.atlassian.net"}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(filepath.Join(dir, "missing.json"))
	if err != nil || !reflect.DeepEqual(cfg, Config{}) || !errors.Is(cfg.Validate(), ErrConfigMissing) {
		t.Fatal(cfg, err)
	}
	good := filepath.Join(dir, "good.json")
	body := `{"gitlabUser":"reviewer.one","jiraEmail":"owner@example.com","jiraSite":"example.atlassian.net","gitlabGroup":"team","issuePrefixes":["PROJ","OPS"]}`
	if err := os.WriteFile(good, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig(good)
	if err != nil || cfg.Validate() != nil || cfg.GitLabUser != "reviewer.one" || cfg.GitLabGroup != "team" || !reflect.DeepEqual(cfg.IssuePrefixes, []string{"PROJ", "OPS"}) {
		t.Fatal(cfg, err)
	}
	extra := filepath.Join(dir, "extra.json")
	if err := os.WriteFile(extra, []byte(`{"gitlabUser":"a","jiraEmail":"a@b","token":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(extra); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestConfigValidate(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	mutate := func(f func(*Config)) Config {
		c := validConfig()
		f(&c)
		return c
	}
	for _, bad := range []Config{
		mutate(func(c *Config) { c.GitLabUser = "a b" }),
		mutate(func(c *Config) { c.GitLabUser = "user&admin=1" }),
		mutate(func(c *Config) { c.JiraEmail = "no-at" }),
		mutate(func(c *Config) { c.JiraEmail = "@b" }),
		mutate(func(c *Config) { c.JiraEmail = "a@" }),
		mutate(func(c *Config) { c.JiraEmail = "a @b" }),
		mutate(func(c *Config) { c.JiraSite = "" }),
		mutate(func(c *Config) { c.JiraSite = "https://example.atlassian.net" }),
		mutate(func(c *Config) { c.JiraSite = "example.atlassian.net/browse" }),
		mutate(func(c *Config) { c.GitLabGroup = "team?x=1" }),
		mutate(func(c *Config) { c.IssuePrefixes = []string{"proj"} }),
		mutate(func(c *Config) { c.IssuePrefixes = []string{"A|B"} }),
		{GitLabUser: "ok"},
	} {
		if bad.Validate() == nil {
			t.Error("accepted", bad)
		}
	}
	if err := mutate(func(c *Config) { c.GitLabGroup = "team/sub"; c.IssuePrefixes = []string{"PROJ", "OPS2"} }).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestProvidersRequireConfig(t *testing.T) {
	calls := 0
	g := &GitLabClient{Command: commandFunc(func(context.Context, Command) ([]byte, error) {
		calls++
		return []byte(`[]`), nil
	})}
	if _, err := g.reviewer(context.Background()); !errors.Is(err, ErrConfigMissing) || calls != 0 {
		t.Fatal(err, calls)
	}
	j := JiraClient{Token: func(context.Context) (string, error) { return "test-only", nil }}
	if _, err := j.Issues(context.Background(), []string{"PROJ-1"}); !errors.Is(err, ErrConfigMissing) {
		t.Fatal(err)
	}
	j.Email = "owner@example.com"
	if _, err := j.Issues(context.Background(), []string{"PROJ-1"}); !errors.Is(err, ErrConfigMissing) {
		t.Fatal("missing origin accepted", err)
	}
}

func TestJiraClientUsesConfiguredOrigin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/api/3/search/jql") {
			t.Error("unexpected path", r.URL.Path)
		}
		fmt.Fprint(w, `{"issues":[{"key":"PROJ-1","fields":{"status":{"name":"Done","statusCategory":{"key":"done"}}}}],"isLast":true}`)
	}))
	t.Cleanup(server.Close)
	j := JiraClient{Origin: server.URL, Email: "owner@example.com", Token: func(context.Context) (string, error) { return "test-only", nil }}
	issues, err := j.Issues(context.Background(), []string{"PROJ-1"})
	if err != nil || issues["PROJ-1"].Category != "done" {
		t.Fatal(issues, err)
	}
}

func TestSearchMRsWithoutGroupMakesNoCall(t *testing.T) {
	calls := 0
	g := &GitLabClient{Command: commandFunc(func(context.Context, Command) ([]byte, error) {
		calls++
		return []byte(`[]`), nil
	})}
	matches, err := g.SearchMRs(context.Background(), "PROJ-12")
	if err != nil || len(matches) != 0 || calls != 0 {
		t.Fatal(matches, err, calls)
	}
	g.Group = "team/sub"
	var path string
	g.Command = commandFunc(func(_ context.Context, c Command) ([]byte, error) {
		path = c.Args[len(c.Args)-1]
		return []byte(`[]`), nil
	})
	if _, err := g.SearchMRs(context.Background(), "PROJ-12"); err != nil || !strings.HasPrefix(path, "groups/team%2Fsub/merge_requests?") {
		t.Fatal(path, err)
	}
}

func TestIssuePrefixes(t *testing.T) {
	task := store.Task{Title: "Fix PROJ-12 and OPS-3", Notes: "XPROJ-9 and MV-1 and PROJ-12-b and PROJ_5", Subtasks: []store.Subtask{{ID: "s1", Title: "see OPS-4"}}}
	if links := textIssueLinks(task, nil); len(links) != 0 {
		t.Fatal("mentions found without prefixes", links)
	}
	refs := func(prefixes []string) []string {
		var out []string
		for _, l := range textIssueLinks(task, prefixes) {
			out = append(out, l.Ref)
		}
		return out
	}
	if got := refs([]string{"PROJ"}); !reflect.DeepEqual(got, []string{"PROJ-12"}) {
		t.Fatal(got)
	}
	if got := refs([]string{"PROJ", "OPS"}); !reflect.DeepEqual(got, []string{"PROJ-12", "OPS-3", "OPS-4"}) {
		t.Fatal(got)
	}
}
