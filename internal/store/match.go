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
	titleKeyMu sync.RWMutex
	titleKey   *regexp.Regexp
	titleMRURL = regexp.MustCompile(`https://gitlab\.com/[A-Za-z0-9_][A-Za-z0-9_./-]*?/-/merge_requests/[1-9][0-9]*`)
	titleMRRef = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_.-]*(?:/[A-Za-z0-9_][A-Za-z0-9_.-]*)+![1-9][0-9]*`)
)

// SetIssuePrefixes sets the Jira projects whose keys are read from card titles; none turns it off.
func SetIssuePrefixes(prefixes []string) error {
	var pattern *regexp.Regexp
	if len(prefixes) > 0 {
		for _, prefix := range prefixes {
			if !issuePrefix.MatchString(prefix) {
				return fmt.Errorf("invalid issue prefix %q: use uppercase Jira project keys such as PROJ", prefix)
			}
		}
		pattern = regexp.MustCompile(`\b(?:` + strings.Join(prefixes, "|") + `)-[1-9][0-9]*\b`)
	}
	titleKeyMu.Lock()
	titleKey = pattern
	titleKeyMu.Unlock()
	return nil
}

func titleKeyPattern() *regexp.Regexp {
	titleKeyMu.RLock()
	defer titleKeyMu.RUnlock()
	return titleKey
}

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

func linkValue(link LinkInput) string {
	if link.Ref != "" {
		return link.Ref
	}
	return link.URL
}

func inputRefKey(link LinkInput) (refKey, bool) {
	kind, ref, _, err := CanonicalLink(linkValue(link))
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

// cardRefs reads the title too, since older cards carry their keys only there.
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

type candidate struct {
	task Task
	ref  string
}

func findMatches(tasks map[string]Task, keys []refKey) (open, done []candidate) {
	if len(keys) == 0 {
		return nil, nil
	}
	for _, task := range tasks {
		refs := cardRefs(task)
		for _, key := range keys {
			if !refs[key] {
				continue
			}
			if task.Lane == "done" {
				done = append(done, candidate{task, key.ref})
			} else {
				open = append(open, candidate{task, key.ref})
			}
			break
		}
	}
	sortByNumber(open)
	sortByNumber(done)
	return open, done
}

func sortByNumber(list []candidate) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].task.Number != list[j].task.Number {
			return list[i].task.Number < list[j].task.Number
		}
		return list[i].task.ID < list[j].task.ID
	})
}

type matchScore struct{ covered, own, number int }

func (a matchScore) beats(b matchScore) bool {
	if a.covered != b.covered {
		return a.covered > b.covered
	}
	if a.own != b.own {
		return a.own < b.own
	}
	return a.number > b.number
}

// pickMatch prefers the card covering the most incoming refs, then the one with the fewest refs of its own.
func pickMatch(open []candidate, keys []refKey) candidate {
	scoreOf := func(match candidate) matchScore {
		refs := cardRefs(match.task)
		covered := 0
		for _, key := range keys {
			if refs[key] {
				covered++
			}
		}
		return matchScore{covered, len(refs), match.task.Number}
	}
	best, bestScore := open[0], scoreOf(open[0])
	for _, match := range open[1:] {
		if score := scoreOf(match); score.beats(bestScore) {
			best, bestScore = match, score
		}
	}
	return best
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
	open, done := findMatches(tasks, keys)
	if len(open) > 0 && onMatch != OnMatchCreate {
		match := pickMatch(open, keys)
		if onMatch == OnMatchReject {
			return CreateOutcome{}, &MatchError{Match{ID: match.task.ID, Number: match.task.Number, Title: match.task.Title, Ref: match.ref}}
		}
		return s.attach(tasks, history, match.task.ID, input, actor)
	}
	if len(done) > 0 {
		input.Notes = followOnNotes(done[len(done)-1].task.Number, input.Notes)
	}
	task, err := s.createTask(tasks, input)
	return CreateOutcome{Task: task}, err
}

func (s *Store) attach(tasks map[string]Task, history *[]HistoryRecord, id string, input TaskInput, actor string) (CreateOutcome, error) {
	normalized, err := normalizeInput(input, defaultTaskSource)
	if err != nil {
		return CreateOutcome{}, err
	}
	lane := input.Lane
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
	if err := s.addMissingLinks(&task, input.Links); err != nil {
		return CreateOutcome{}, err
	}
	task.UpdatedAt = s.now()
	tasks[id] = task
	updated, _, err := s.appendWorkLog(tasks, *history, WorkLogInput{
		TaskID: id, SubtaskID: subtask.ID, Kind: WorkLogProgress, Text: attachNote(normalized),
	}, Mutation{Actor: actor, TaskID: id})
	if err != nil {
		return CreateOutcome{}, err
	}
	*history = updated
	return CreateOutcome{Task: task, Attached: true, Subtask: &subtask}, nil
}

func (s *Store) addMissingLinks(task *Task, links *[]LinkInput) error {
	if links == nil {
		return nil
	}
	has := map[refKey]bool{}
	for _, link := range task.Links {
		has[refKey{link.Kind, link.Ref}] = true
	}
	for _, link := range *links {
		key, ok := inputRefKey(link)
		if !ok {
			return fmt.Errorf("invalid Jira/MR reference %q", linkValue(link))
		}
		if has[key] {
			continue
		}
		has[key] = true
		if _, err := s.upsertLink(task, LinkInput{Ref: key.ref, Role: link.Role}); err != nil {
			return err
		}
	}
	return nil
}

// attachNote keeps everything the caller sent, since only the title becomes the step.
func attachNote(input TaskInput) string {
	text := "attached from " + input.Source + ": " + input.Title
	if input.Notes != "" {
		text += "\n\n" + input.Notes
	}
	if len(input.Subtasks) > 0 {
		titles := make([]string, len(input.Subtasks))
		for i, sub := range input.Subtasks {
			titles[i] = sub.Title
		}
		text += "\n\nSteps: " + strings.Join(titles, "; ")
	}
	return truncateRunes(text, MaxWorkLogText)
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
					break
				}
			}
		}
	}
	return outcomes, nil
}
