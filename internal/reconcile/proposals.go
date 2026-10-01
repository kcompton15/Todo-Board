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
	"unicode"
	"unicode/utf8"
)

type ModelResult struct {
	Status       string     `json:"status"`
	Truncated    bool       `json:"truncated"`
	Hypothetical bool       `json:"hypothetical"`
	Envelopes    []Envelope `json:"envelopes"`
	Rejections   []string   `json:"rejections"`
}

var secretText = regexp.MustCompile(`(?i)(bearer\s+\S+|(?:api[_-]?key|token|password|secret)\s*[:=]\s*\S+)`)

func sanitized(text string, max int) string {
	text = secretText.ReplaceAllString(text, "[REDACTED]")
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
	runes := []rune(text)
	if len(runes) > max {
		return string(runes[:max]) + " [truncated]"
	}
	return text
}

func modelBundle(tasks []store.Task, evidence Evidence, history HistoryEvidence, hypothetical bool) ([]byte, bool, error) {
	type card struct {
		Task    store.Task            `json:"task"`
		History []store.HistoryRecord `json:"history"`
	}
	bundle := struct {
		Hypothetical bool     `json:"hypothetical"`
		Cards        []card   `json:"cards"`
		Evidence     Evidence `json:"evidence"`
	}{Hypothetical: hypothetical, Cards: []card{}, Evidence: Evidence{}}
	truncated := false
	for _, task := range tasks {
		if !eligible(task) {
			continue
		}
		if len(bundle.Cards) == 40 {
			truncated = true
			break
		}
		task.Notes = sanitized(task.Notes, 4000)
		task.AgentContext = sanitized(task.AgentContext, 4000)
		task.Title = sanitized(task.Title, 500)
		task.Subtasks = append([]store.Subtask{}, task.Subtasks...)
		for i := range task.Subtasks {
			task.Subtasks[i].Title = sanitized(task.Subtasks[i].Title, 500)
		}
		records := []store.HistoryRecord{}
		for _, r := range history[task.ID].Records {
			if len(records) == 30 {
				truncated = true
				break
			}
			// History IDs and typed operational logs are sufficient; omit arbitrary diffs.
			r.Changes = nil
			r.Title = sanitized(r.Title, 500)
			if r.WorkLog != nil {
				copy := *r.WorkLog
				copy.Text = sanitized(copy.Text, 1000)
				r.WorkLog = &copy
			}
			records = append(records, r)
		}
		bundle.Cards = append(bundle.Cards, card{task, records})
		for _, link := range task.Links {
			observed, ok := evidence[link.Ref]
			if !ok {
				continue
			}
			if observed.MR != nil {
				copy := *observed.MR
				copy.Title = sanitized(copy.Title, 500)
				copy.SourceBranch = sanitized(copy.SourceBranch, 200)
				observed.MR = &copy
			}
			if observed.Review != nil {
				copy := *observed.Review
				copy.Events = append([]ReviewEvent{}, copy.Events...)
				if len(copy.Events) > 50 {
					copy.Events = copy.Events[:50]
					truncated = true
				}
				for i := range copy.Events {
					copy.Events[i].Body = sanitized(copy.Events[i].Body, 2000)
				}
				observed.Review = &copy
			}
			bundle.Evidence[link.Ref] = observed
		}
	}
	data, err := json.Marshal(bundle)
	if len(data) > maxModelInput {
		return nil, true, fmt.Errorf("model bundle exceeds %d bytes; model skipped", maxModelInput)
	}
	return data, truncated, err
}

func proposalEvidence(task store.Task, p Proposal, evidence Evidence, history CardHistory) bool {
	if len(p.EvidenceIDs) == 0 || len(p.EvidenceIDs) > 30 {
		return false
	}
	allowed := map[string]bool{}
	for _, link := range task.Links {
		if link.SubtaskID != p.Scope {
			continue
		}
		observed, ok := evidence[link.Ref]
		if !ok || observed.Unavailable != "" {
			continue
		}
		if observed.MR != nil || observed.Issue != nil {
			allowed[link.Ref] = true
		}
		if observed.Review != nil && observed.Review.Complete {
			for _, event := range observed.Review.Events {
				allowed[event.ID] = true
			}
		}
	}
	for _, r := range history.Records {
		if r.SubtaskID == p.Scope && r.WorkLog != nil && r.Actor != "board-reconciler" {
			allowed[r.ID] = true
			allowed[r.WorkLog.ID] = true
		}
	}
	seen := map[string]bool{}
	for _, id := range p.EvidenceIDs {
		if id == "" || !allowed[id] || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

var sourceURL = regexp.MustCompile(`https://[^\s<>"'\)\]]+`)

func exactSourceRef(source, ref string) bool {
	for _, raw := range sourceURL.FindAllString(source, -1) {
		_, canonical, _, err := store.CanonicalLink(strings.TrimRight(raw, ".,;"))
		if err == nil && canonical == ref {
			return true
		}
	}
	return false
}

func validateProposals(tasks []store.Task, evidence Evidence, history HistoryEvidence, proposals Proposals) ModelResult {
	result := ModelResult{Status: "validated", Envelopes: []Envelope{}, Rejections: []string{}}
	if len(proposals.Proposals) > 40 {
		result.Rejections = append(result.Rejections, "proposal limit exceeded; entire response rejected")
		return result
	}
	byID := map[string]store.Task{}
	for _, task := range tasks {
		byID[task.ID] = task
	}
	ops := map[string][]store.Operation{}
	reasons := map[string][]string{}
	seen := map[string]bool{}
	invalid := map[string]bool{}
	for _, p := range proposals.Proposals {
		reject := func(reason string) { result.Rejections = append(result.Rejections, p.TaskID+": "+reason) }
		task, ok := byID[p.TaskID]
		h, hasHistory := history[p.TaskID]
		if !ok || !eligible(task) || !hasHistory {
			reject("card unavailable or excluded")
			continue
		}
		scopeExists := p.Scope == ""
		for _, s := range task.Subtasks {
			if s.ID == p.Scope {
				scopeExists = true
			}
		}
		if !scopeExists || !proposalEvidence(task, p, evidence, h) {
			reject("scope/evidence mismatch")
			continue
		}
		key := p.TaskID + "/" + p.Scope + "/" + p.Kind
		if p.Kind == "reference" {
			key += "/" + p.Value
		}
		if seen[key] {
			invalid[p.TaskID] = true
			reject("duplicate/conflicting suggestions reject card batch")
			continue
		}
		seen[key] = true
		if !utf8.ValidString(p.Value) || utf8.RuneCountInString(p.Value) > 4000 {
			reject("proposal text over limit")
			continue
		}
		// Timestamp-free signature stops the model rewording unchanged evidence every run.
		ids := append([]string{}, p.EvidenceIDs...)
		sort.Strings(ids)
		facts := map[string]Observation{}
		for _, link := range task.Links {
			if link.SubtaskID == p.Scope {
				v := evidence[link.Ref]
				v.FetchedAt = time.Time{}
				facts[link.Ref] = v
			}
		}
		humanLogs := []store.WorkLogEntry{}
		for _, r := range h.Records {
			if r.WorkLog != nil && r.SubtaskID == p.Scope && r.Actor != "board-reconciler" && r.Actor != "board-notifier" {
				humanLogs = append(humanLogs, *r.WorkLog)
			}
		}
		raw, _ := json.Marshal(struct {
			Kind, Scope string
			Boundary    time.Time
			History     []store.WorkLogEntry
			Facts       map[string]Observation
		}{p.Kind, p.Scope, boundary(task, p.Scope, h), humanLogs, facts})
		digest := sha256.Sum256(raw)
		marker := "model-evidence:" + hex.EncodeToString(digest[:])
		duplicate := false
		for _, r := range h.Records {
			if r.WorkLog != nil && r.Actor == "board-reconciler" && strings.Contains(r.WorkLog.Text, marker) {
				duplicate = true
			}
		}
		if duplicate {
			continue
		}
		reason := "model suggestion (not completion evidence) " + marker + " evidence=" + strings.Join(ids, ",")
		switch p.Kind {
		case "lane":
			reject("lane suggestion report-only: " + sanitized(p.Value, 50))
			continue
		case "check":
			if p.Scope == "" {
				reject("parent completion forbidden")
				continue
			}
			done, proof := scopeDone(task, p.Scope, evidence, h)
			if !done {
				reject("child completion not independently proven")
				continue
			}
			already := false
			for _, s := range task.Subtasks {
				if s.ID == p.Scope && s.Done {
					already = true
				}
			}
			if already {
				continue
			}
			ops[task.ID] = append(ops[task.ID], store.Operation{Op: "check", ID: task.ID, SubtaskID: p.Scope})
			reason += " " + proof
		case "reference":
			kind, ref, _, err := store.CanonicalLink(p.Value)
			if err != nil {
				reject("invalid reference")
				continue
			}
			// References need an exact URL already supplied for this scope.
			source := task.Title + "\n" + task.Notes
			if p.Scope != "" {
				source = ""
				for _, s := range task.Subtasks {
					if s.ID == p.Scope {
						source = s.Title
					}
				}
			}
			observed, exists := evidence[ref]
			if !exists || observed.Unavailable != "" || !exactSourceRef(source, ref) || (kind == "mr" && observed.MR == nil) || (kind == "jira" && observed.Issue == nil) {
				reject("reference mapping not independently established")
				continue
			}
			exists = false
			for _, l := range task.Links {
				if l.Ref == ref && l.SubtaskID == p.Scope {
					exists = true
				}
			}
			if exists {
				continue
			}
			if len(task.Links)+len(ops[task.ID]) >= store.MaxLinks {
				reject("link limit exceeded")
				continue
			}
			ops[task.ID] = append(ops[task.ID], store.Operation{Op: "link", ID: task.ID, Link: &store.LinkInput{Ref: ref, Role: "reference", SubtaskID: p.Scope}})
		case "agent_context":
			if p.Scope != "" {
				reject("memo is card-scoped")
				continue
			}
			if task.AgentContext == p.Value {
				continue
			}
			value := sanitized(p.Value, 4000)
			ops[task.ID] = append(ops[task.ID], store.Operation{Op: "patch", ID: task.ID, AgentContext: &value})
		case "work_log":
			if strings.TrimSpace(p.Value) == "" {
				reject("empty log")
				continue
			}
			reason += " scope=" + p.Scope + " text=" + sanitized(p.Value, 2000)
		default:
			reject("unsupported proposal kind")
			continue
		}
		reasons[task.ID] = append(reasons[task.ID], reason)
	}
	for _, task := range tasks {
		if invalid[task.ID] || len(reasons[task.ID]) == 0 {
			continue
		}
		if len(ops[task.ID])+1 > 100 || utf8.RuneCountInString(strings.Join(reasons[task.ID], "\n")) > 8000 {
			result.Rejections = append(result.Rejections, task.ID+": generated batch exceeds limits")
			continue
		}
		result.Envelopes = append(result.Envelopes, makeEnvelope(task, history[task.ID], ops[task.ID], reasons[task.ID]))
	}
	return result
}
