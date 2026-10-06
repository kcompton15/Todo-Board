package store

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	OnMatchAttach = "attach"
	OnMatchReject = "reject"
	OnMatchCreate = "create"
)

func ValidOnMatch(policy string) bool {
	return policy == OnMatchAttach || policy == OnMatchReject || policy == OnMatchCreate
}

var issuePrefix = regexp.MustCompile(`^[A-Z][A-Z0-9]+$`)

var (
	titleKeyMu    sync.RWMutex
	titleIssueKey *regexp.Regexp
)

// ParseIssuePrefixes reads a comma-separated list such as "PROJ,OPS".
func ParseIssuePrefixes(value string) ([]string, error) {
	var prefixes []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !issuePrefix.MatchString(part) {
			return nil, fmt.Errorf("invalid issue prefix %q: use uppercase Jira project keys such as PROJ", part)
		}
		prefixes = append(prefixes, part)
	}
	return prefixes, nil
}

// SetIssuePrefixes sets the Jira projects whose keys are read from card titles; none disables it.
func SetIssuePrefixes(prefixes []string) error {
	var pattern *regexp.Regexp
	if len(prefixes) > 0 {
		quoted := make([]string, len(prefixes))
		for i, prefix := range prefixes {
			if !issuePrefix.MatchString(prefix) {
				return fmt.Errorf("invalid issue prefix %q: use uppercase Jira project keys such as PROJ", prefix)
			}
			quoted[i] = regexp.QuoteMeta(prefix)
		}
		pattern = regexp.MustCompile(`\b(?:` + strings.Join(quoted, "|") + `)-[1-9][0-9]*\b`)
	}
	titleKeyMu.Lock()
	titleIssueKey = pattern
	titleKeyMu.Unlock()
	return nil
}

func titleKeyPattern() *regexp.Regexp {
	titleKeyMu.RLock()
	defer titleKeyMu.RUnlock()
	return titleIssueKey
}

var (
	titleMRURL = regexp.MustCompile(`https://gitlab\.com/[A-Za-z0-9_][A-Za-z0-9_./-]*?/-/merge_requests/[1-9][0-9]*`)
	titleMRRef = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_.-]*(?:/[A-Za-z0-9_][A-Za-z0-9_.-]*)+![1-9][0-9]*`)
)

type Match struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Ref    string `json:"ref"`
}

type MatchError struct {
	Match Match
}

func (e *MatchError) Error() string {
	return fmt.Sprintf("#%d already covers %s; use onMatch=attach to add a step there or onMatch=create for a separate card", e.Match.Number, e.Match.Ref)
}

type CreateOutcome struct {
	Task     Task
	Attached bool
	Subtask  *Subtask
}

type refKey struct{ kind, ref string }

func titleRefs(title string) []LinkInput {
	type found struct {
		at  int
		ref string
	}
	var hits []found
	if pattern := titleKeyPattern(); pattern != nil {
		for _, span := range pattern.FindAllStringIndex(title, -1) {
			if adjacent(title, span, "-", "-") {
				continue
			}
			hits = append(hits, found{span[0], title[span[0]:span[1]]})
		}
	}
	for _, span := range titleMRURL.FindAllStringIndex(title, -1) {
		hits = append(hits, found{span[0], title[span[0]:span[1]]})
	}
	for _, span := range titleMRRef.FindAllStringIndex(title, -1) {
		if adjacent(title, span, "./:-", "") {
			continue
		}
		hits = append(hits, found{span[0], title[span[0]:span[1]]})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].at < hits[j].at })
	result := make([]LinkInput, 0, len(hits))
	seen := map[refKey]bool{}
	for _, hit := range hits {
		kind, ref, _, err := CanonicalLink(hit.ref)
		if err != nil || seen[refKey{kind, ref}] {
			continue
		}
		seen[refKey{kind, ref}] = true
		result = append(result, LinkInput{Ref: ref, Role: "required"})
	}
	return result
}

func adjacent(text string, span []int, before, after string) bool {
	word := func(r rune) bool {
		return r == '_' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
	}
	if span[0] > 0 {
		r, _ := utf8.DecodeLastRuneInString(text[:span[0]])
		if word(r) || strings.ContainsRune(before, r) {
			return true
		}
	}
	if span[1] < len(text) {
		r, _ := utf8.DecodeRuneInString(text[span[1]:])
		if word(r) || strings.ContainsRune(after, r) {
			return true
		}
	}
	return false
}

func inputRefKey(link LinkInput) (refKey, bool) {
	value := link.Ref
	if value == "" {
		value = link.URL
	}
	kind, ref, _, err := CanonicalLink(value)
	if err != nil {
		return refKey{}, false
	}
	return refKey{kind, ref}, true
}

func withTitleRefs(input TaskInput) TaskInput {
	extracted := titleRefs(input.Title)
	if len(extracted) == 0 {
		return input
	}
	var links []LinkInput
	present := map[refKey]bool{}
	if input.Links != nil {
		links = append(links, (*input.Links)...)
		for _, link := range links {
			if key, ok := inputRefKey(link); ok {
				present[key] = true
			}
		}
	}
	for _, link := range extracted {
		key, _ := inputRefKey(link)
		if !present[key] {
			present[key] = true
			links = append(links, link)
		}
	}
	input.Links = &links
	return input
}

func incomingRequired(input TaskInput) []refKey {
	if input.Links == nil {
		return nil
	}
	var keys []refKey
	seen := map[refKey]bool{}
	for _, link := range *input.Links {
		if link.Role != "" && link.Role != "required" {
			continue
		}
		key, ok := inputRefKey(link)
		if ok && !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys
}

// Legacy cards carry their keys only in titles.
func cardRefs(task Task) map[refKey]bool {
	refs := map[refKey]bool{}
	for _, link := range task.Links {
		if link.Role == "required" {
			refs[refKey{link.Kind, link.Ref}] = true
		}
	}
	for _, link := range titleRefs(task.Title) {
		if key, ok := inputRefKey(link); ok {
			refs[key] = true
		}
	}
	return refs
}

func findMatches(tasks map[string]Task, keys []refKey) (open, done []Task, covered map[string]string) {
	covered = map[string]string{}
	for _, task := range tasks {
		refs := cardRefs(task)
		for _, key := range keys {
			if refs[key] {
				covered[task.ID] = key.ref
				break
			}
		}
		if _, ok := covered[task.ID]; !ok {
			continue
		}
		if task.Lane == "done" {
			done = append(done, task)
		} else {
			open = append(open, task)
		}
	}
	byNumber := func(list []Task) {
		sort.Slice(list, func(i, j int) bool {
			if list[i].Number != list[j].Number {
				return list[i].Number < list[j].Number
			}
			return list[i].ID < list[j].ID
		})
	}
	byNumber(open)
	byNumber(done)
	return open, done, covered
}

func pickMatch(candidates []Task, refs []string) (Task, bool) {
	if len(candidates) == 0 {
		return Task{}, false
	}
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.UpdatedAt.After(best.UpdatedAt) || (candidate.UpdatedAt.Equal(best.UpdatedAt) && candidate.ID > best.ID) {
			best = candidate
		}
	}
	return best, true
}

func followOnNotes(number int, notes string) string {
	prefix := fmt.Sprintf("Follow-on to #%d.", number)
	notes = strings.TrimSpace(notes)
	if strings.HasPrefix(notes, prefix) {
		return notes
	}
	if notes == "" {
		return prefix
	}
	room := MaxNotesLength - utf8.RuneCountInString(prefix) - 2
	return prefix + "\n\n" + truncateRunes(notes, room)
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func (s *Store) createOrAttach(tasks map[string]Task, history *[]HistoryRecord, input TaskInput, onMatch, actor string) (CreateOutcome, error) {
	if !ValidOnMatch(onMatch) {
		return CreateOutcome{}, errors.New("onMatch must be attach, reject, or create")
	}
	input = withTitleRefs(input)
	keys := incomingRequired(input)
	if len(keys) > 0 {
		open, done, covered := findMatches(tasks, keys)
		if onMatch != OnMatchCreate {
			refs := make([]string, len(keys))
			for i, key := range keys {
				refs[i] = key.ref
			}
			if match, ok := pickMatch(open, refs); ok {
				if onMatch == OnMatchReject {
					return CreateOutcome{}, &MatchError{Match{ID: match.ID, Number: match.Number, Title: match.Title, Ref: covered[match.ID]}}
				}
				return s.attach(tasks, history, match.ID, input, actor)
			}
		}
		if len(done) > 0 {
			input.Notes = followOnNotes(done[len(done)-1].Number, input.Notes)
		}
	}
	task, err := s.createTask(tasks, input)
	return CreateOutcome{Task: task}, err
}

func (s *Store) attach(tasks map[string]Task, history *[]HistoryRecord, id string, input TaskInput, actor string) (CreateOutcome, error) {
	lane := input.Lane
	normalized, err := normalizeInput(input, defaultTaskSource)
	if err != nil {
		return CreateOutcome{}, err
	}
	if lane == "" {
		lane = "today"
	}
	task := cloneTask(tasks[id])
	if len(task.Subtasks) >= MaxSubtasks {
		return CreateOutcome{}, fmt.Errorf("#%d already has %d steps; cannot attach another", task.Number, MaxSubtasks)
	}
	step, err := normalizeSubtaskInput(SubtaskInput{Title: truncateRunes(normalized.Title, MaxSubtaskTitle), Lane: lane}, task.Lane)
	if err != nil {
		return CreateOutcome{}, err
	}
	subtaskID, err := s.newSubtaskID()
	if err != nil {
		return CreateOutcome{}, err
	}
	subtask := Subtask{ID: subtaskID, Number: takeSubtaskNumber(&task), Title: step.Title, Lane: step.Lane, Done: step.Done}
	task.Subtasks = append(task.Subtasks, subtask)
	if input.Links != nil {
		has := map[refKey]bool{}
		for _, link := range task.Links {
			has[refKey{link.Kind, link.Ref}] = true
		}
		for _, link := range *input.Links {
			key, ok := inputRefKey(link)
			if !ok {
				return CreateOutcome{}, fmt.Errorf("invalid Jira/MR reference %q", link.Ref+link.URL)
			}
			if has[key] {
				continue
			}
			has[key] = true
			role := link.Role
			if role == "" {
				role = "required"
			}
			if _, err := s.upsertLink(&task, LinkInput{Ref: key.ref, Role: role}); err != nil {
				return CreateOutcome{}, err
			}
		}
	}
	task.UpdatedAt = s.now()
	tasks[id] = task
	text := "attached from " + normalized.Source + ": " + normalized.Title
	if normalized.Notes != "" {
		text += "\n\n" + normalized.Notes
	}
	if len(normalized.Subtasks) > 0 {
		titles := make([]string, len(normalized.Subtasks))
		for i, sub := range normalized.Subtasks {
			titles[i] = sub.Title
		}
		text += "\n\nSteps: " + strings.Join(titles, "; ")
	}
	updated, _, err := s.appendWorkLog(tasks, *history, WorkLogInput{
		TaskID: id, SubtaskID: subtask.ID, Kind: WorkLogProgress, Text: truncateRunes(text, MaxWorkLogText),
	}, Mutation{Actor: actor, TaskID: id})
	if err != nil {
		return CreateOutcome{}, err
	}
	*history = updated
	return CreateOutcome{Task: task, Attached: true, Subtask: &subtask}, nil
}

func (s *Store) CreateOrAttach(inputs []TaskInput, sourceOverride, onMatch string, options ...Mutation) ([]CreateOutcome, error) {
	if len(inputs) == 0 {
		return nil, errors.New("at least one task is required")
	}
	if len(options) == 0 {
		options = []Mutation{{Actor: sourceOverride}}
	}
	var outcomes []CreateOutcome
	var committed map[string]Task
	err := s.transactWithHistory(func(tasks map[string]Task, history *[]HistoryRecord) error {
		committed = tasks
		outcomes = make([]CreateOutcome, 0, len(inputs))
		for i, input := range inputs {
			if sourceOverride != "" {
				input.Source = sourceOverride
			}
			outcome, err := s.createOrAttach(tasks, history, input, onMatch, options[0].Actor)
			if err != nil {
				return fmt.Errorf("tasks[%d]: %w", i, err)
			}
			outcomes = append(outcomes, outcome)
		}
		return nil
	}, options...)
	if err != nil {
		return nil, err
	}
	for i := range outcomes {
		task := cloneTask(committed[outcomes[i].Task.ID])
		outcomes[i].Task = task
		if outcomes[i].Subtask != nil {
			for _, sub := range task.Subtasks {
				if sub.ID == outcomes[i].Subtask.ID {
					found := sub
					outcomes[i].Subtask = &found
				}
			}
		}
	}
	return outcomes, nil
}
