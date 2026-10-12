package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // Keep America/Chicago calendar reporting correct in the minimal Alpine container.
	"unicode/utf8"
)

const (
	MaxEvidenceEntryText = 500
	MaxEvidenceBytes     = 96 * 1024
	digestDetailRunes    = 160
	digestMaxObserved    = 4
)

type ReportRequest struct {
	Kind     string   `json:"kind"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	Projects []string `json:"projects,omitempty"`
}

type ReportPreview struct {
	Kind                 string   `json:"kind"`
	Start                string   `json:"start"`
	End                  string   `json:"end"`
	Markdown             string   `json:"markdown"`
	SourceRecordIDs      []string `json:"sourceRecordIds"`
	PartialLegacyHistory bool     `json:"partialLegacyHistory"`
}

// Evidence is the deterministic per-card packet a report is written from.
type Evidence struct {
	Kind                 string            `json:"kind"`
	Start                string            `json:"start"`
	End                  string            `json:"end"`
	Projects             []string          `json:"projects,omitempty"`
	Cards                []EvidenceCard    `json:"cards"`
	OpenBlockers         []EvidenceBlocker `json:"openBlockers"`
	ActiveCards          []EvidenceCardRef `json:"activeCards,omitempty"`
	Truncated            bool              `json:"truncated"`
	PartialLegacyHistory bool              `json:"partialLegacyHistory"`
	SourceRecordIDs      []string          `json:"-"`
}

type EvidenceCard struct {
	Number           int            `json:"number"`
	Title            string         `json:"title"`
	Project          string         `json:"project"`
	Kind             string         `json:"kind"`
	Lane             string         `json:"lane"`
	DoneInWindow     bool           `json:"doneInWindow"`
	StepsChecked     []string       `json:"stepsChecked,omitempty"`
	Observed         []string       `json:"observed,omitempty"`
	Progress         []EvidenceNote `json:"progress,omitempty"`
	ReviewsDelivered []EvidenceNote `json:"reviewsDelivered,omitempty"`
	Decisions        []EvidenceNote `json:"decisions,omitempty"`
	Blockers         []EvidenceNote `json:"blockers,omitempty"`
	BlockersResolved []EvidenceNote `json:"blockersResolved,omitempty"`
	NextSteps        []EvidenceNote `json:"nextSteps,omitempty"`
}

type EvidenceNote struct {
	At          time.Time `json:"at"`
	Text        string    `json:"text"`
	NeedsReview bool      `json:"needsReview,omitempty"`
}

type EvidenceBlocker struct {
	Number int       `json:"number"`
	Title  string    `json:"title"`
	At     time.Time `json:"at"`
	Text   string    `json:"text"`
}

type EvidenceCardRef struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Project string `json:"project"`
	Lane    string `json:"lane"`
}

var (
	observedLine   = regexp.MustCompile(`^observed (\S+) (.+)$`)
	mergedPart     = regexp.MustCompile(`^merged (\S+)$`)
	discoveredLine = regexp.MustCompile(`^discovered reference:`)
)

// PreviewReport renders recorded evidence as a grouped digest. It makes no model call.
func (s *Store) PreviewReport(request ReportRequest) (ReportPreview, error) {
	evidence, err := s.ReportEvidence(request)
	if err != nil {
		return ReportPreview{}, err
	}
	return ReportPreview{Kind: evidence.Kind, Start: evidence.Start, End: evidence.End, Markdown: RenderDigest(evidence), SourceRecordIDs: evidence.SourceRecordIDs, PartialLegacyHistory: evidence.PartialLegacyHistory}, nil
}

func (s *Store) ReportEvidence(request ReportRequest) (Evidence, error) {
	if request.Kind != "standup" && request.Kind != "weekly" {
		return Evidence{}, errors.New("report kind must be standup or weekly")
	}
	start, end, err := ChicagoDateRange(request.Start, request.End)
	if err != nil {
		return Evidence{}, err
	}
	projects := make(map[string]bool)
	projectList := make([]string, 0, len(request.Projects))
	for _, project := range request.Projects {
		if trimmed := strings.TrimSpace(project); trimmed != "" && !projects[trimmed] {
			projects[trimmed] = true
			projectList = append(projectList, trimmed)
		}
	}
	evidence := Evidence{Kind: request.Kind, Start: request.Start, End: request.End, Cards: []EvidenceCard{}, OpenBlockers: []EvidenceBlocker{}, SourceRecordIDs: []string{}}
	if len(projectList) > 0 {
		evidence.Projects = projectList
	}
	inScope := func(task Task) bool {
		return task.Kind != "probe" && (len(projects) == 0 || projects[task.Project])
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	evidence.PartialLegacyHistory = s.partialLegacyHistory

	selected := make([]HistoryRecord, 0)
	for _, record := range s.history {
		activityAt := record.At
		if record.WorkLog != nil {
			activityAt = record.WorkLog.OccurredAt
		}
		if activityAt.Before(start) || !activityAt.Before(end) {
			continue
		}
		task, ok := s.tasks[record.TaskID]
		if !ok || !inScope(task) {
			continue
		}
		selected = append(selected, record)
	}
	sort.SliceStable(selected, func(i, j int) bool { return recordActivityAt(selected[i]).Before(recordActivityAt(selected[j])) })

	builders := make(map[string]*cardBuilder)
	order := make([]string, 0)
	for _, record := range selected {
		builder, ok := builders[record.TaskID]
		if !ok {
			task := s.tasks[record.TaskID]
			builder = newCardBuilder(task)
			builders[record.TaskID] = builder
			order = append(order, record.TaskID)
		}
		builder.add(record)
	}
	reported := make(map[string]bool)
	for _, id := range order {
		builder := builders[id]
		card := builder.finish()
		if !card.hasFacts() {
			continue
		}
		reported[id] = true
		evidence.Cards = append(evidence.Cards, card)
		evidence.SourceRecordIDs = append(evidence.SourceRecordIDs, builder.recordIDs...)
	}
	sort.SliceStable(evidence.Cards, func(i, j int) bool {
		a, b := evidence.Cards[i], evidence.Cards[j]
		if a.Project != b.Project {
			return projectSortKey(a.Project) < projectSortKey(b.Project)
		}
		return a.Number < b.Number
	})

	resolved := resolvedBlockers(s.history)
	for _, record := range s.history {
		if record.WorkLog == nil || record.WorkLog.Kind != WorkLogBlocker || resolved[record.WorkLog.ID] {
			continue
		}
		task, ok := s.tasks[record.TaskID]
		if !ok || !inScope(task) || task.Lane == "done" {
			continue
		}
		evidence.OpenBlockers = append(evidence.OpenBlockers, EvidenceBlocker{Number: task.Number, Title: task.Title, At: record.WorkLog.OccurredAt, Text: capText(record.WorkLog.Text)})
	}

	if request.Kind == "weekly" {
		for _, task := range s.tasks {
			if (task.Lane != "doing" && task.Lane != "blocked") || !inScope(task) || reported[task.ID] {
				continue
			}
			evidence.ActiveCards = append(evidence.ActiveCards, EvidenceCardRef{Number: task.Number, Title: task.Title, Project: task.Project, Lane: task.Lane})
		}
		sort.Slice(evidence.ActiveCards, func(i, j int) bool { return evidence.ActiveCards[i].Number < evidence.ActiveCards[j].Number })
	}

	if err := fitEvidence(&evidence, MaxEvidenceBytes); err != nil {
		return Evidence{}, err
	}
	return evidence, nil
}

func recordActivityAt(record HistoryRecord) time.Time {
	if record.WorkLog != nil {
		return record.WorkLog.OccurredAt
	}
	return record.At
}

func projectSortKey(project string) string {
	if project == "" {
		return "￿"
	}
	return strings.ToLower(project)
}

type cardBuilder struct {
	card         EvidenceCard
	recordIDs    []string
	subtaskTitle map[string]string
	observed     map[string]string
	observedRefs []string
	seenLines    map[string]bool
	seenSteps    map[string]bool
}

func newCardBuilder(task Task) *cardBuilder {
	titles := make(map[string]string, len(task.Subtasks))
	for _, sub := range task.Subtasks {
		titles[sub.ID] = sub.Title
	}
	return &cardBuilder{
		card:         EvidenceCard{Number: task.Number, Title: task.Title, Project: task.Project, Kind: task.Kind, Lane: task.Lane},
		subtaskTitle: titles,
		observed:     make(map[string]string),
		seenLines:    make(map[string]bool),
		seenSteps:    make(map[string]bool),
	}
}

func (b *cardBuilder) add(record HistoryRecord) {
	b.recordIDs = append(b.recordIDs, record.ID)
	if record.WorkLog == nil {
		for _, change := range record.Changes {
			b.addChange(change)
		}
		return
	}
	entry := record.WorkLog
	kept := make([]string, 0)
	for _, line := range strings.Split(entry.Text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || discoveredLine.MatchString(line) {
			continue
		}
		if match := observedLine.FindStringSubmatch(line); match != nil {
			b.observe(match[1], match[2])
			continue
		}
		if b.observeMerged(line) {
			continue
		}
		key := string(entry.Kind) + "\x00" + line
		if b.seenLines[key] {
			continue
		}
		b.seenLines[key] = true
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return
	}
	note := EvidenceNote{At: entry.OccurredAt, Text: capText(strings.Join(kept, "\n")), NeedsReview: entry.NeedsReview}
	switch entry.Kind {
	case WorkLogProgress:
		b.card.Progress = append(b.card.Progress, note)
	case WorkLogReviewDelivered:
		b.card.ReviewsDelivered = append(b.card.ReviewsDelivered, note)
	case WorkLogDecision:
		b.card.Decisions = append(b.card.Decisions, note)
	case WorkLogBlocker:
		b.card.Blockers = append(b.card.Blockers, note)
	case WorkLogBlockerResolved:
		b.card.BlockersResolved = append(b.card.BlockersResolved, note)
	case WorkLogNextStep:
		b.card.NextSteps = append(b.card.NextSteps, note)
	}
}

func (b *cardBuilder) addChange(change FieldChange) {
	if change.Field == "lane" && change.After == "done" {
		b.card.DoneInWindow = true
		return
	}
	if !strings.HasPrefix(change.Field, "subtask ") || !strings.HasSuffix(change.Field, " lane") || change.After != "done" {
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(change.Field, "subtask "), " lane")
	title, ok := b.subtaskTitle[id]
	if !ok || b.seenSteps[id] {
		return
	}
	b.seenSteps[id] = true
	b.card.StepsChecked = append(b.card.StepsChecked, title)
}

func (b *cardBuilder) observe(ref, state string) {
	ref = shortRef(ref)
	if _, ok := b.observed[ref]; !ok {
		b.observedRefs = append(b.observedRefs, ref)
	}
	b.observed[ref] = strings.TrimSpace(state)
}

func (b *cardBuilder) observeMerged(line string) bool {
	parts := strings.Split(line, ";")
	refs := make([]string, 0, len(parts))
	for _, part := range parts {
		match := mergedPart.FindStringSubmatch(strings.TrimSpace(part))
		if match == nil {
			return false
		}
		refs = append(refs, match[1])
	}
	for _, ref := range refs {
		b.observe(ref, "merged")
	}
	return true
}

func (b *cardBuilder) finish() EvidenceCard {
	for _, ref := range b.observedRefs {
		b.card.Observed = append(b.card.Observed, ref+" "+b.observed[ref])
	}
	return b.card
}

// hasFacts is false for cards whose only changes in the window were edits such as rank or project.
func (card EvidenceCard) hasFacts() bool {
	return card.DoneInWindow || len(card.StepsChecked) > 0 || len(card.Observed) > 0 || len(card.Progress) > 0 || len(card.ReviewsDelivered) > 0 ||
		len(card.Decisions) > 0 || len(card.Blockers) > 0 || len(card.BlockersResolved) > 0 || len(card.NextSteps) > 0
}

// shortRef drops the GitLab group path so "group/repo!12" reads as "repo!12".
func shortRef(ref string) string {
	if !strings.ContainsAny(ref, "!#") {
		return ref
	}
	if index := strings.LastIndex(ref, "/"); index >= 0 {
		return ref[index+1:]
	}
	return ref
}

func capText(text string) string {
	return ellipsize(strings.TrimSpace(text), MaxEvidenceEntryText)
}

func ellipsize(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return strings.TrimSpace(truncateRunes(text, limit-3)) + "..."
}

type noteRef struct {
	card  int
	slice *[]EvidenceNote
	at    time.Time
	text  string
	other bool
}

// fitEvidence drops the oldest progress notes first, then the oldest of any other notes.
func fitEvidence(evidence *Evidence, limit int) error {
	size, err := evidenceSize(evidence)
	if err != nil || size <= limit {
		return err
	}
	refs := make([]noteRef, 0)
	for i := range evidence.Cards {
		card := &evidence.Cards[i]
		for _, list := range []*[]EvidenceNote{&card.Progress, &card.ReviewsDelivered, &card.Decisions, &card.Blockers, &card.BlockersResolved, &card.NextSteps} {
			for _, note := range *list {
				refs = append(refs, noteRef{card: i, slice: list, at: note.At, text: note.Text, other: list != &card.Progress})
			}
		}
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].other != refs[j].other {
			return !refs[i].other
		}
		return refs[i].at.Before(refs[j].at)
	})
	evidence.Truncated = true
	for _, ref := range refs {
		list := *ref.slice
		for index, note := range list {
			if note.At.Equal(ref.at) && note.Text == ref.text {
				encoded, err := json.Marshal(note)
				if err != nil {
					return fmt.Errorf("encode report evidence: %w", err)
				}
				size -= len(encoded) + 1
				*ref.slice = append(list[:index:index], list[index+1:]...)
				break
			}
		}
		if size > limit {
			continue
		}
		if size, err = evidenceSize(evidence); err != nil || size <= limit {
			return err
		}
	}
	return nil
}

func evidenceSize(evidence *Evidence) (int, error) {
	data, err := json.Marshal(evidence)
	if err != nil {
		return 0, fmt.Errorf("encode report evidence: %w", err)
	}
	return len(data), nil
}

// RenderDigest renders evidence as grouped Markdown a person can read without a model.
func RenderDigest(evidence Evidence) string {
	title := "Weekly report"
	if evidence.Kind == "standup" {
		title = "Standup"
	}
	lines := []string{fmt.Sprintf("# %s (%s through %s)", title, evidence.Start, evidence.End)}
	if len(evidence.Cards) == 0 {
		lines = append(lines, "", "No recorded work-log or task-change evidence falls in this period.")
	}
	project := "\x00"
	for _, card := range evidence.Cards {
		if card.Project != project {
			project = card.Project
			heading := project
			if heading == "" {
				heading = "No project"
			}
			lines = append(lines, "", "## "+heading)
		}
		lines = append(lines, "- "+cardLabel(card.Number, card.Title)+cardStatus(card))
		for _, detail := range cardDetails(card) {
			lines = append(lines, "  - "+detail)
		}
	}
	if len(evidence.OpenBlockers) > 0 {
		lines = append(lines, "", "## Blockers")
		latest := make(map[int]int)
		for index, blocker := range evidence.OpenBlockers {
			if current, ok := latest[blocker.Number]; !ok || !blocker.At.Before(evidence.OpenBlockers[current].At) {
				latest[blocker.Number] = index
			}
		}
		for index, blocker := range evidence.OpenBlockers {
			if latest[blocker.Number] == index {
				lines = append(lines, "- "+cardLabel(blocker.Number, blocker.Title)+": "+oneLine(blocker.Text))
			}
		}
	}
	nextSteps := make([]string, 0)
	for _, card := range evidence.Cards {
		if card.Lane == "done" || len(card.NextSteps) == 0 {
			continue
		}
		latest := card.NextSteps[len(card.NextSteps)-1]
		nextSteps = append(nextSteps, "- "+cardLabel(card.Number, card.Title)+": "+oneLine(latest.Text)+sourceSuffix(latest.NeedsReview))
	}
	if len(nextSteps) > 0 {
		lines = append(lines, "", "## Next steps")
		lines = append(lines, nextSteps...)
	}
	if len(evidence.ActiveCards) > 0 {
		labels := make([]string, 0, len(evidence.ActiveCards))
		for _, card := range evidence.ActiveCards {
			labels = append(labels, cardLabel(card.Number, card.Title))
		}
		lines = append(lines, "", "## Also in flight", strings.Join(labels, "; "))
	}
	if evidence.Truncated {
		lines = append(lines, "", "_Older progress notes were left out to keep this short._")
	}
	return strings.Join(lines, "\n")
}

func cardLabel(number int, title string) string {
	if number > 0 {
		return fmt.Sprintf("#%d %s", number, title)
	}
	return title
}

func cardStatus(card EvidenceCard) string {
	switch {
	case card.DoneInWindow || card.Lane == "done":
		return " (done)"
	case card.Lane == "blocked":
		return " (blocked)"
	default:
		return ""
	}
}

func cardDetails(card EvidenceCard) []string {
	details := make([]string, 0, 3)
	if note, ok := latestNote(card.ReviewsDelivered, card.Progress); ok {
		details = append(details, oneLine(note.Text)+sourceSuffix(note.NeedsReview))
	}
	if len(card.Observed) > 0 {
		shown := card.Observed
		suffix := ""
		if len(shown) > digestMaxObserved {
			suffix = fmt.Sprintf(" (+%d more)", len(shown)-digestMaxObserved)
			shown = shown[:digestMaxObserved]
		}
		details = append(details, "Observed: "+strings.Join(shown, ", ")+suffix)
	}
	if len(card.StepsChecked) > 0 {
		details = append(details, oneLine("Checked: "+strings.Join(card.StepsChecked, "; ")))
	}
	if len(details) < 3 && len(card.Decisions) > 0 {
		details = append(details, oneLine("Decided: "+card.Decisions[len(card.Decisions)-1].Text))
	}
	if len(details) > 3 {
		details = details[:3]
	}
	return details
}

func latestNote(lists ...[]EvidenceNote) (EvidenceNote, bool) {
	var latest EvidenceNote
	found := false
	for _, list := range lists {
		for _, note := range list {
			if !found || note.At.After(latest.At) {
				latest, found = note, true
			}
		}
	}
	return latest, found
}

func oneLine(text string) string {
	return ellipsize(strings.Join(strings.Fields(text), " "), digestDetailRunes)
}

func sourceSuffix(needsReview bool) string {
	if needsReview {
		return " _(needs review)_"
	}
	return ""
}

func resolvedBlockers(history []HistoryRecord) map[string]bool {
	resolved := make(map[string]bool)
	for _, record := range history {
		if record.WorkLog != nil && record.WorkLog.ResolvesBlockerID != "" {
			resolved[record.WorkLog.ResolvesBlockerID] = true
		}
	}
	return resolved
}
