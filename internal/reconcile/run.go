package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/kcompton15/Todo-Board/internal/store"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type RunOptions struct {
	DryRun    bool
	TaskID    string
	Since     time.Time
	StatePath string
	NoLLM     bool
}
type RunResult struct {
	EvidenceFailures   []string      `json:"evidenceFailures"`
	Model              ModelResult   `json:"model"`
	RecentHumanUpdates int           `json:"recentHumanUpdates"`
	Plan               PlanResult    `json:"plan"`
	Changed            int           `json:"changed"`
	Replayed           int           `json:"replayed"`
	Conflicted         int           `json:"conflicted"`
	Unavailable        int           `json:"unavailable"`
	Skipped            int           `json:"skipped"`
	Errors             []string      `json:"errors"`
	DryRun             bool          `json:"dryRun"`
	CursorAdvanced     bool          `json:"cursorAdvanced"`
	Gaps               []EvidenceGap `json:"gaps"`
	ChangedCards       []CardRef     `json:"changedCards"`
}

// Absent: provider answered without the identity. Required: a scope depends on it.
type EvidenceGap struct {
	Identity string `json:"identity"`
	Reason   string `json:"reason"`
	Absent   bool   `json:"absent"`
	Required bool   `json:"required"`
}
type CardRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type Runner struct {
	Model  Assistant
	Board  *Board
	GitLab GitLab
	Jira   Jira
	Now    func() time.Time

	IssuePrefixes []string
}

func (r *Runner) Run(ctx context.Context, options RunOptions) (RunResult, error) {
	unlock, err := Lock(LockPath(options.StatePath, r.Board.Origin))
	if err != nil {
		return RunResult{DryRun: options.DryRun, Errors: []string{}}, err
	}
	defer unlock()
	return r.run(ctx, options)
}

// Beside the state file because launchd agents get no TMPDIR, unlike interactive shells.
func LockPath(statePath, origin string) string {
	id := sha256.Sum256([]byte(origin))
	return filepath.Join(filepath.Dir(statePath), "board-reconciler-"+hex.EncodeToString(id[:6])+".lock")
}

func (r *Runner) run(ctx context.Context, options RunOptions) (RunResult, error) {
	result := RunResult{DryRun: options.DryRun, Errors: []string{}, Gaps: []EvidenceGap{}, ChangedCards: []CardRef{}}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	gitlab := &gitlabBreaker{inner: r.GitLab}
	state, err := LoadState(options.StatePath, r.Board.Origin)
	if err != nil {
		return result, err
	}
	startHistory, err := r.Board.History(ctx, "")
	if err != nil {
		return result, err
	}
	for _, record := range startHistory.Records {
		if record.ID == state.Cursor && state.Cursor != "" {
			break
		}
		if !options.Since.IsZero() && record.At.Before(options.Since) {
			continue
		}
		if record.Actor != "board-reconciler" && record.Actor != "board-notifier" {
			result.RecentHumanUpdates++
		}
	}
	if state.Cursor != "" {
		found := false
		for _, item := range startHistory.Records {
			if item.ID == state.Cursor {
				found = true
				break
			}
		}
		if !found {
			result.Errors = append(result.Errors, "saved cursor missing from bounded history; cursor retained")
		}
	}
	tasks, err := r.Board.Tasks(ctx)
	if err != nil {
		return result, err
	}
	selected := []store.Task{}
	for _, task := range tasks {
		if options.TaskID != "" && task.ID != options.TaskID {
			continue
		}
		if !eligible(task) {
			result.Skipped++
			continue
		}
		selected = append(selected, task)
	}
	if options.TaskID != "" {
		found := false
		for _, task := range tasks {
			if task.ID == options.TaskID {
				found = true
			}
		}
		if !found {
			return result, errors.New("requested card not found")
		}
	}
	if len(selected) > 200 {
		return result, errors.New("run exceeds 200 eligible cards; use --task")
	}
	history := HistoryEvidence{}
	evidence := Evidence{}
	issueKeys := map[string]bool{}
	mrRefs := map[string]bool{}
	reviewRefs := map[string]bool{}
	discoveries := map[string][]store.Link{}
	lookupLinks := map[string][]store.Link{}
	requiredRefs := map[string]bool{}
	titles := map[string]string{}
	for _, task := range selected {
		titles[task.ID] = task.Title
		h, err := r.Board.History(ctx, task.ID)
		if err != nil {
			result.Errors = append(result.Errors, task.ID+": history unavailable")
			result.Unavailable++
			continue
		}
		history[task.ID] = h
		links, _ := legacyLinks(task)
		links = append(links, task.Links...)
		discoveries[task.ID] = textIssueLinks(task, r.IssuePrefixes)
		links = append(links, discoveries[task.ID]...)
		lookupLinks[task.ID] = links
		for _, link := range links {
			if link.Role == "required" {
				requiredRefs[link.Ref] = true
			}
			if link.Kind == "jira" {
				issueKeys[link.Ref] = true
			} else {
				mrRefs[link.Ref] = true
				if task.Kind == "review" {
					reviewRefs[link.Ref] = true
				}
			}
		}
	}
	keys := []string{}
	for key := range issueKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 100 || len(mrRefs) > 200 {
		return result, errors.New("provider identity budget exceeded; use --task")
	}
	// Only required transient failures can change a decision.
	gap := func(identity, reason string, absent, required bool) {
		result.Gaps = append(result.Gaps, EvidenceGap{Identity: identity, Reason: reason, Absent: absent, Required: required})
		result.EvidenceFailures = append(result.EvidenceFailures, identity+": "+reason)
		if required && !absent {
			result.Unavailable++
		}
	}
	for _, key := range keys {
		matches, err := gitlab.SearchMRs(ctx, key)
		if err != nil {
			gap(key, "GitLab title search unavailable", false, false)
			continue
		}
		for _, task := range selected {
			for _, link := range lookupLinks[task.ID] {
				if link.Ref != key {
					continue
				}
				for _, mr := range matches {
					ref := fmt.Sprintf("%s!%d", mr.Project, mr.IID)
					copy := mr
					evidence[ref] = Observation{MR: &copy, FetchedAt: now()}
					discoveries[task.ID] = append(discoveries[task.ID], store.Link{Kind: "mr", Ref: ref, URL: mr.WebURL, SubtaskID: link.SubtaskID, Role: "reference"})
				}
			}
		}
	}
	if len(keys) > 0 {
		issues, err := r.Jira.Issues(ctx, keys)
		// An answer that omits every requested key proves nothing about the key.
		authoritative := err == nil && len(issues) > 0
		for _, key := range keys {
			observation := Observation{FetchedAt: now()}
			if err != nil {
				observation.Unavailable = "Jira fetch failed"
			} else if issue, ok := issues[key]; ok {
				observation.Issue = &issue
			} else {
				observation.Unavailable = "Jira issue missing"
			}
			if observation.Unavailable != "" {
				gap(key, observation.Unavailable, observation.Issue == nil && authoritative, requiredRefs[key])
			}
			evidence[key] = observation
		}
	}
	refs := []string{}
	for ref := range mrRefs {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		split := strings.LastIndex(ref, "!")
		if split < 0 {
			result.Unavailable++
			continue
		}
		iid, err := strconv.Atoi(ref[split+1:])
		if err != nil {
			result.Unavailable++
			continue
		}
		observation := Observation{FetchedAt: now()}
		mr, err := gitlab.MR(ctx, ref[:split], iid)
		if err != nil {
			observation.Unavailable = "MR fetch failed"
			gap(ref, "MR fetch unavailable", false, requiredRefs[ref])
		} else {
			observation.MR = &mr
			if reviewRefs[ref] && mr.State != "merged" && mr.State != "closed" {
				activity, err := gitlab.ReviewActivity(ctx, ref[:split], iid)
				if err != nil {
					gap(ref, "review activity unavailable", false, requiredRefs[ref])
				} else {
					observation.Review = &activity
				}
			}
		}
		evidence[ref] = observation
	}
	safeTasks := []store.Task{}
	for _, task := range selected {
		if _, ok := history[task.ID]; ok {
			safeTasks = append(safeTasks, task)
		}
	}
	result.Plan = Plan(safeTasks, evidence, history, discoveries)
	for _, envelope := range result.Plan.Envelopes {
		if options.DryRun {
			continue
		}
		replayed, err := r.Board.Apply(ctx, envelope)
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.Status == 409 {
				result.Conflicted++
			} else {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", envelope.TaskID, err))
			}
			continue
		}
		if replayed {
			result.Replayed++
		} else {
			result.Changed++
			if completes(envelope) {
				result.ChangedCards = append(result.ChangedCards, CardRef{ID: envelope.TaskID, Title: titles[envelope.TaskID]})
			}
		}
	}
	r.modelPhase(ctx, options, safeTasks, evidence, history, &result)
	if !options.DryRun && options.TaskID == "" && result.Unavailable == 0 && result.Conflicted == 0 && len(result.Errors) == 0 {
		state.Cursor = startHistory.Head
		if err := SaveState(options.StatePath, state); err != nil {
			return result, err
		}
		result.CursorAdvanced = true
	}
	return result, nil
}

// Stops GitLab calls after three consecutive failures so an outage cannot starve Jira-only cards.
type gitlabBreaker struct {
	inner    GitLab
	failures int
}

var errGitLabBreaker = errors.New("GitLab skipped after repeated failures")

func (g *gitlabBreaker) observe(err error) error {
	if err == nil {
		g.failures = 0
	} else {
		g.failures++
	}
	return err
}
func (g *gitlabBreaker) open() bool { return g.failures >= 3 }
func (g *gitlabBreaker) MR(ctx context.Context, project string, iid int) (MRState, error) {
	if g.open() {
		return MRState{}, errGitLabBreaker
	}
	mr, err := g.inner.MR(ctx, project, iid)
	return mr, g.observe(err)
}
func (g *gitlabBreaker) SearchMRs(ctx context.Context, key string) ([]MRState, error) {
	if g.open() {
		return nil, errGitLabBreaker
	}
	mrs, err := g.inner.SearchMRs(ctx, key)
	return mrs, g.observe(err)
}
func (g *gitlabBreaker) ReviewActivity(ctx context.Context, project string, iid int) (ReviewActivity, error) {
	if g.open() {
		return ReviewActivity{}, errGitLabBreaker
	}
	activity, err := g.inner.ReviewActivity(ctx, project, iid)
	return activity, g.observe(err)
}

// Link cache refreshes and reference discoveries are routine; only checks and lane moves are news.
func completes(envelope Envelope) bool {
	for _, op := range envelope.Batch.Ops {
		if op.Op == "check" || (op.Op == "patch" && op.Lane != nil) {
			return true
		}
	}
	return false
}
