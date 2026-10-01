package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/kcompton15/Todo-Board/internal/reconcile"
	"github.com/kcompton15/Todo-Board/internal/store"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
	_ "time/tzdata"
)

const maxLogBytes = 1 << 20

// errLogged marks a scheduled failure already in the rotated log.
var errLogged = errors.New("logged")

func main() {
	if err := run(); err != nil {
		if !errors.Is(err, errLogged) {
			fmt.Fprintln(os.Stderr, "board-reconciler:", err)
		}
		os.Exit(1)
	}
}
func run() error {
	dry := flag.Bool("dry-run", false, "plan without board/state writes")
	task := flag.String("task", "", "limit to one full card ID")
	origin := flag.String("todo-url", "http://127.0.0.1:7337", "loopback board origin")
	since := flag.String("since", "", "RFC3339 history cutoff (never skips provider checks)")
	noLLM := flag.Bool("no-llm", false, "skip optional model assistance")
	scheduled := flag.Bool("scheduled", false, "launchd mode: run only when a weekday Chicago slot is due")
	status := flag.Bool("status", false, "print schedule/cursor state without provider calls")
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	state := flag.String("state-path", filepath.Join(home, ".local", "state", "todo-board", "board-reconciler.json"), "state file")
	logPath := flag.String("log-file", filepath.Join(home, ".local", "log", "board-reconciler.log"), "scheduled-run log")
	configPath := flag.String("config", reconcile.DefaultConfigPath(home), "reconciler config file")
	gitlabUser := flag.String("gitlab-user", "", "GitLab username whose reviews count (overrides --config)")
	jiraEmail := flag.String("jira-email", "", "Jira account email and Keychain account (overrides --config)")
	jiraSite := flag.String("jira-site", "", "Jira hostname such as example.atlassian.net (overrides --config)")
	gitlabGroup := flag.String("gitlab-group", "", "GitLab group searched for MR titles (overrides --config)")
	issuePrefixes := flag.String("issue-prefixes", "", "comma-separated Jira project keys found in card text (overrides --config)")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if *scheduled && (*dry || *task != "" || *since != "" || *status) {
		return fmt.Errorf("--scheduled cannot be combined with --dry-run, --task, --since or --status")
	}
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		return err
	}
	board, err := reconcile.NewBoard(*origin, nil)
	if err != nil {
		return err
	}
	if *status {
		return printStatus(board.Origin, *state, chicago)
	}
	var cutoff time.Time
	if *since != "" {
		cutoff, err = time.Parse(time.RFC3339, *since)
		if err != nil {
			return fmt.Errorf("invalid --since timestamp")
		}
	}
	cfg, err := reconcile.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	if *gitlabUser != "" {
		cfg.GitLabUser = *gitlabUser
	}
	if *jiraEmail != "" {
		cfg.JiraEmail = *jiraEmail
	}
	if *jiraSite != "" {
		cfg.JiraSite = *jiraSite
	}
	if *gitlabGroup != "" {
		cfg.GitLabGroup = *gitlabGroup
	}
	if *issuePrefixes != "" {
		cfg.IssuePrefixes = strings.Split(*issuePrefixes, ",")
	}
	if err := cfg.Validate(); err != nil {
		if !*scheduled {
			return err
		}
		// Scheduled runs surface a bad config as provider unavailability, which notifies once per episode.
		cfg = reconcile.Config{}
	}
	if err := store.SetJiraSite(cfg.JiraSite); err != nil {
		return err
	}
	jiraOrigin := ""
	if cfg.JiraSite != "" {
		jiraOrigin = "https://" + cfg.JiraSite
	}
	command := reconcile.ExecCommander{}
	runner := reconcile.Runner{Board: board, GitLab: &reconcile.GitLabClient{Command: command, Username: cfg.GitLabUser, Group: cfg.GitLabGroup}, Jira: &reconcile.JiraClient{Command: command, Email: cfg.JiraEmail, Origin: jiraOrigin}, Model: reconcile.Claude{Command: command}, IssuePrefixes: cfg.IssuePrefixes}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if *scheduled {
		scheduler := reconcile.Scheduler{Runner: &runner, Location: chicago, Now: time.Now, Notifier: reconcile.OSANotifier{Command: command}, StatePath: *state, NoLLM: *noLLM}
		result, err := scheduler.Tick(ctx)
		if err == nil && !result.Ran {
			return nil
		}
		return appendLog(*logPath, result, err)
	}
	result, err := runner.Run(ctx, reconcile.RunOptions{DryRun: *dry, TaskID: *task, Since: cutoff, StatePath: *state, NoLLM: *noLLM})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return err
	}
	if len(result.Errors) > 0 || result.Unavailable > 0 || result.Conflicted > 0 {
		return fmt.Errorf("run incomplete; see counts and cursor status")
	}
	return nil
}

func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return reconcile.RuleVersion
	}
	revision, dirty := "", ""
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			if setting.Value == "true" {
				dirty = "+dirty"
			}
		}
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	return reconcile.RuleVersion + "@" + revision + dirty
}

func appendLog(path string, result reconcile.TickResult, tickErr error) error {
	entry := struct {
		reconcile.TickResult
		Version string `json:"version"`
		Fatal   string `json:"fatal,omitempty"`
	}{TickResult: result, Version: version()}
	if tickErr != nil {
		entry.Fatal = tickErr.Error()
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogBytes {
		if err := os.Rename(path, path+".1"); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if tickErr != nil {
		return errLogged
	}
	return nil
}

func printStatus(origin, statePath string, loc *time.Location) error {
	saved, err := reconcile.LoadState(statePath, origin)
	if err != nil {
		return err
	}
	now := time.Now()
	out := map[string]any{
		"origin":    origin,
		"statePath": statePath,
		"cursor":    saved.Cursor,
		"now":       now.In(loc).Format(time.RFC3339),
		"nextSlot":  reconcile.NextSlot(now, loc).Format(time.RFC3339),
		"schedule":  saved.Schedule,
		"degraded":  saved.Schedule != nil && saved.Schedule.DegradedSince != nil,
		"version":   version(),
	}
	if due, ok := reconcile.DueSlot(now, loc); ok {
		out["dueSlot"] = due.Format(time.RFC3339)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(out)
}
