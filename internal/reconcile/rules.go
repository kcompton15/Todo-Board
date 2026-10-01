package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/kcompton15/Todo-Board/internal/store"
	"regexp"
	"sort"
	"strings"
	"time"
)

func eligible(task store.Task) bool {
	return task.Lane != "done" && task.ReconcileMode != "manual" && (task.Kind == "work" || task.Kind == "review")
}
func required(links []store.Link, scope string) []store.Link {
	out := []store.Link{}
	for _, l := range links {
		if l.Role == "required" && l.SubtaskID == scope {
			out = append(out, l)
		}
	}
	return out
}
func requiredTargets(raw string, scope string) string {
	var links []store.Link
	if json.Unmarshal([]byte(raw), &links) != nil {
		return "invalid"
	}
	ids := []string{}
	for _, l := range required(links, scope) {
		ids = append(ids, l.Kind+":"+l.Ref)
	}
	sort.Strings(ids)
	return strings.Join(ids, "\n")
}

var reviewRequest = regexp.MustCompile(`(?i)\b(re-review|rereview|review again|new review|reopen review|requested review)\b`)

func boundary(task store.Task, scope string, history CardHistory) time.Time {
	start := task.CreatedAt
	for _, r := range history.Records {
		reset := false
		if r.WorkLog != nil {
			log := r.WorkLog
			if (log.SubtaskID == "" || log.SubtaskID == scope) && (log.Kind == store.WorkLogBlocker || ((log.Kind == store.WorkLogDecision || log.Kind == store.WorkLogNextStep) && reviewRequest.MatchString(log.Text))) {
				reset = true
			}
		}
		for _, c := range r.Changes {
			if c.Field == "kind" || c.Field == "title" {
				reset = true
			}
			if c.Field == "lane" && c.Before == "done" && c.After != "done" {
				reset = true
			}
			if c.Field == "links" && r.Actor != "board-reconciler" && requiredTargets(c.Before, scope) != requiredTargets(c.After, scope) {
				reset = true
			}
			if strings.HasPrefix(c.Field, "subtask ") {
				parts := strings.Fields(c.Field)
				if len(parts) < 2 {
					continue
				}
				child := parts[1]
				if child == scope && (len(parts) == 2 || strings.HasSuffix(c.Field, " title") || (strings.HasSuffix(c.Field, " lane") && c.Before == "done" && c.After != "done")) {
					reset = true
				}
				if scope == "" && ((len(parts) == 2 && c.Before == "") || (strings.HasSuffix(c.Field, " lane") && c.Before == "done" && c.After != "done")) {
					reset = true
				}
			}
		}
		if reset && r.At.After(start) {
			start = r.At
		}
	}
	return start
}

func blocked(task store.Task, scope string, history CardHistory) bool {
	blockers := map[string]store.WorkLogEntry{}
	resolved := map[string]time.Time{}
	resolvedAny := false
	var pausedAt time.Time
	for _, r := range history.Records {
		for _, c := range r.Changes {
			if c.After == "blocked" && (c.Field == "lane" || (scope != "" && c.Field == "subtask "+scope+" lane")) && r.At.After(pausedAt) {
				pausedAt = r.At
			}
		}
	}
	for _, r := range history.Records {
		if r.WorkLog == nil {
			continue
		}
		l := r.WorkLog
		if l.Kind == store.WorkLogBlockerResolved && l.ResolvesBlockerID != "" {
			if r.At.After(resolved[l.ResolvesBlockerID]) {
				resolved[l.ResolvesBlockerID] = r.At
			}
		}
		if l.Kind == store.WorkLogBlocker && (l.SubtaskID == "" || l.SubtaskID == scope) {
			blockers[l.ID] = *l
		}
	}
	for id, blocker := range blockers {
		if at, ok := resolved[id]; !ok || at.Before(blocker.RecordedAt) {
			return true
		}
		if !resolved[id].Before(pausedAt) {
			resolvedAny = true
		}
	}
	lane := task.Lane
	if scope != "" {
		for _, sub := range task.Subtasks {
			if sub.ID == scope {
				lane = sub.Lane
			}
		}
	}
	return (lane == "blocked" || task.Lane == "blocked") && !resolvedAny
}

var findingWords = regexp.MustCompile(`(?i)\b(bug|fails?|failure|incorrect|missing|breaks?|broken|panic|race|leak|drops?|lost|loses|cannot|does not|doesn't|will not|won't|must|should|needs? to)\b`)
var verdictWords = regexp.MustCompile(`(?i)^(review complete|no findings|no issues found|looks good to me|lgtm|changes requested)([.!,:](\s|$)|$)`)
var pendingReview = regexp.MustCompile(`(?i)\b(not reviewed|not yet|haven't|have not|will review|to review|review later|review tomorrow|still reviewing|not complete|incomplete|so far|remaining files|partial review)\b`)

func substantive(event ReviewEvent) bool {
	if event.System || event.Bot || len(strings.TrimSpace(event.Body)) < 8 {
		return false
	}
	text := strings.TrimSpace(event.Body)
	if pendingReview.MatchString(text) {
		return false
	}
	if verdictWords.MatchString(text) {
		return true
	}
	if strings.HasSuffix(text, "?") {
		return false
	}
	return event.Diff && len(text) >= 25 && (findingWords.MatchString(text) || strings.Contains(text, "```suggestion"))
}

func scopeDone(task store.Task, scope string, evidence Evidence, history CardHistory) (bool, string) {
	if blocked(task, scope, history) {
		return false, "unresolved blocker or human pause"
	}
	start := boundary(task, scope, history)
	if task.Kind == "review" && history.Complete {
		for _, r := range history.Records {
			l := r.WorkLog
			if l != nil && l.Kind == store.WorkLogReviewDelivered && !l.NeedsReview && l.SubtaskID == scope && !l.RecordedAt.Before(start) {
				return true, "review delivery receipt " + l.ID
			}
		}
	}
	links := required(task.Links, scope)
	mrs, issues := []store.Link{}, []store.Link{}
	for _, l := range links {
		if l.Kind == "mr" {
			mrs = append(mrs, l)
		} else {
			issues = append(issues, l)
		}
	}
	if len(mrs) > 0 {
		reasons := []string{}
		for _, link := range mrs {
			observed, ok := evidence[link.Ref]
			if !ok || observed.Unavailable != "" || observed.MR == nil {
				return false, "MR evidence unavailable: " + link.Ref
			}
			if observed.MR.State == "merged" {
				label := "merged "
				if task.Kind == "review" {
					label = "review target terminal (merged) "
				}
				reasons = append(reasons, label+link.Ref)
				continue
			}
			if task.Kind != "review" {
				return false, "required MR not merged: " + link.Ref
			}
			if observed.MR.State == "closed" {
				reasons = append(reasons, "review target terminal "+link.Ref)
				continue
			}
			activity := observed.Review
			if activity == nil || !activity.Complete || activity.ReviewerID == 0 {
				return false, "review activity incomplete: " + link.Ref
			}
			matched := ""
			for _, event := range activity.Events {
				if event.AuthorID != activity.ReviewerID || event.Bot || event.CreatedAt == nil || event.CreatedAt.Before(start) {
					continue
				}
				if event.Kind == "approval" || (event.Kind == "comment" && substantive(event)) {
					matched = fmt.Sprintf("%s %s reviewer=%d at=%s evidence=%s %s", link.Ref, event.Kind, event.AuthorID, event.CreatedAt.UTC().Format(time.RFC3339Nano), event.ID, event.URL)
					break
				}
			}
			if matched == "" {
				return false, "no matching delivered review evidence: " + link.Ref
			}
			reasons = append(reasons, matched)
		}
		return true, strings.Join(reasons, "; ")
	}
	if task.Kind == "review" {
		return false, "review has no qualifying MR evidence or scoped receipt"
	}
	if len(issues) == 0 {
		return false, "no required completion links"
	}
	for _, link := range issues {
		observed, ok := evidence[link.Ref]
		if !ok || observed.Unavailable != "" || observed.Issue == nil || observed.Issue.Category != "done" {
			return false, "required Jira issue not confirmed Done: " + link.Ref
		}
	}
	return true, "all required Jira issues Done"
}

// Cache values on tasks never count as evidence.
func Plan(tasks []store.Task, evidence Evidence, history HistoryEvidence, discoveries ...map[string][]store.Link) PlanResult {
	result := PlanResult{Envelopes: []Envelope{}, Decisions: []Decision{}}
	for _, original := range tasks {
		if !eligible(original) {
			continue
		}
		task := original
		task.Links = append([]store.Link{}, original.Links...)
		h := history[task.ID]
		ops := []store.Operation{}
		reasons := []string{}
		migrated, ambiguities := legacyLinks(task)
		if len(discoveries) > 0 {
			migrated = append(migrated, discoveries[0][task.ID]...)
		}
		for _, message := range ambiguities {
			result.Decisions = append(result.Decisions, Decision{TaskID: task.ID, Reason: message})
		}
		for _, link := range migrated {
			exists := false
			for _, old := range task.Links {
				if old.Ref == link.Ref && old.SubtaskID == link.SubtaskID {
					exists = true
				}
			}
			if exists {
				continue
			}
			if len(task.Links) >= store.MaxLinks {
				result.Decisions = append(result.Decisions, Decision{TaskID: task.ID, Reason: "discovery exceeds link limit"})
				continue
			}
			copy := store.LinkInput{Ref: link.Ref, Role: link.Role, SubtaskID: link.SubtaskID}
			ops = append(ops, store.Operation{Op: "link", ID: task.ID, Link: &copy})
			task.Links = append(task.Links, link)
			provenance := "discovered reference"
			if link.Role == "required" {
				provenance = "legacy review target from explicit URL"
			}
			reasons = append(reasons, provenance+": "+link.Ref+" scope="+link.SubtaskID)
		}
		for _, link := range task.Links {
			observed, ok := evidence[link.Ref]
			if !ok || observed.Unavailable != "" {
				continue
			}
			state, category := "", ""
			if observed.MR != nil {
				state = observed.MR.State
			}
			if observed.Issue != nil {
				state = observed.Issue.Status
				category = observed.Issue.Category
			}
			if state != "" && (link.State != state || link.StateCategory != category) {
				input := store.LinkInput{Ref: link.Ref, SubtaskID: link.SubtaskID, State: &state, StateCategory: &category}
				ops = append(ops, store.Operation{Op: "link", ID: task.ID, Link: &input})
				reasons = append(reasons, "observed "+link.Ref+" "+state)
			}
		}
		allChildren := true
		for _, sub := range task.Subtasks {
			if sub.Done {
				continue
			}
			done, reason := scopeDone(task, sub.ID, evidence, h)
			result.Decisions = append(result.Decisions, Decision{TaskID: task.ID, Scope: sub.ID, Done: done, Reason: reason})
			if done {
				ops = append(ops, store.Operation{Op: "check", ID: task.ID, SubtaskID: sub.ID})
				reasons = append(reasons, sub.ID+": "+reason)
			} else {
				allChildren = false
			}
		}
		done, reason := scopeDone(task, "", evidence, h)
		if len(required(task.Links, "")) == 0 && len(task.Subtasks) > 0 && allChildren && !blocked(task, "", h) {
			done = true
			reason = "all child outcomes completed"
		}
		done = done && allChildren
		result.Decisions = append(result.Decisions, Decision{TaskID: task.ID, Done: done, Reason: reason})
		if done {
			lane := "done"
			ops = append(ops, store.Operation{Op: "patch", ID: task.ID, Lane: &lane})
			reasons = append(reasons, reason)
		}
		if len(ops) > 0 {
			result.Envelopes = append(result.Envelopes, makeEnvelope(original, h, ops, reasons))
		}
	}
	return result
}

func makeEnvelope(task store.Task, history CardHistory, ops []store.Operation, reasons []string) Envelope {
	for i := range ops {
		revision := task.Revision
		ops[i].ExpectedRevision = &revision
	}
	// JSON encoding of these fixed structs cannot fail (no unsupported values).
	identity, _ := json.Marshal(struct {
		Version  string
		Task     string
		Revision uint64
		Ops      []store.Operation
		Reasons  []string
	}{RuleVersion, task.ID, task.Revision, ops, reasons})
	digest := sha256.Sum256(identity)
	actionID := RuleVersion + "-" + hex.EncodeToString(digest[:])
	revision := task.Revision
	ops = append(ops, store.Operation{Op: "work_log", ID: task.ID, ExpectedRevision: &revision, WorkLog: &store.WorkLogInput{Kind: store.WorkLogProgress, Text: strings.Join(reasons, "\n"), ActionID: actionID, Origin: "board-reconciler"}})
	return Envelope{TaskID: task.ID, Batch: store.OpsEnvelope{Source: "board-reconciler", Ops: ops, ExpectedHistoryHeads: map[string]string{task.ID: history.Head}}, Reasons: reasons}
}
