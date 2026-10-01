package store

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const maxHistoryPage = 100

func fullHistoryChanges(before, after map[string]Task, actor string, now func() time.Time) []HistoryRecord {
	ids := make([]string, 0, len(before)+len(after))
	seen := make(map[string]bool)
	for id := range before {
		seen[id] = true
		ids = append(ids, id)
	}
	for id := range after {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	result := make([]HistoryRecord, 0, len(ids))
	for _, id := range ids {
		old, hadOld := before[id]
		next, hadNext := after[id]
		if hadOld && hadNext && taskEqual(old, next) {
			continue
		}
		title := old.Title
		kind := "task_updated"
		if !hadOld {
			title, kind = next.Title, "task_created"
		}
		if !hadNext {
			kind = "task_deleted"
		}
		result = append(result, HistoryRecord{ID: "h" + fmt.Sprintf("%d", now().UnixNano()) + id, TaskID: id, At: now(), Actor: normalizedActor(actor), Kind: kind, Title: title, Changes: fullTaskChanges(old, next)})
	}
	return result
}

func taskEqual(a, b Task) bool {
	if a.AgentContext != b.AgentContext || a.ReconcileMode != b.ReconcileMode || linksJSON(a.Links) != linksJSON(b.Links) {
		return false
	}
	return a.Revision == b.Revision && a.Title == b.Title && a.Lane == b.Lane && a.Kind == b.Kind && a.Priority == b.Priority && a.Project == b.Project && a.Tag == b.Tag && a.Notes == b.Notes && a.Source == b.Source && a.Order == b.Order && fmt.Sprintf("%v", a.Subtasks) == fmt.Sprintf("%v", b.Subtasks)
}

func fullTaskChanges(before, after Task) []FieldChange {
	changes := make([]FieldChange, 0)
	add := func(name, old, next string) {
		if old != next {
			changes = append(changes, FieldChange{Field: name, Before: old, After: next})
		}
	}
	add("title", before.Title, after.Title)
	add("lane", before.Lane, after.Lane)
	add("kind", before.Kind, after.Kind)
	add("priority", fmt.Sprint(before.Priority), fmt.Sprint(after.Priority))
	add("project", before.Project, after.Project)
	add("tag", before.Tag, after.Tag)
	add("notes", before.Notes, after.Notes)
	add("links", linksJSON(before.Links), linksJSON(after.Links))
	add("agentContext", before.AgentContext, after.AgentContext)
	add("reconcileMode", before.ReconcileMode, after.ReconcileMode)
	add("source", before.Source, after.Source)
	add("rank", fmt.Sprint(before.Order), fmt.Sprint(after.Order))
	oldSubs := make(map[string]Subtask)
	for _, sub := range before.Subtasks {
		oldSubs[sub.ID] = sub
	}
	for _, sub := range after.Subtasks {
		old, ok := oldSubs[sub.ID]
		if !ok {
			add("subtask "+sub.ID, "", sub.Title+" ["+sub.Lane+"]")
		} else {
			add("subtask "+sub.ID+" title", old.Title, sub.Title)
			add("subtask "+sub.ID+" lane", old.Lane, sub.Lane)
		}
		delete(oldSubs, sub.ID)
	}
	for _, sub := range oldSubs {
		add("subtask "+sub.ID, sub.Title+" ["+sub.Lane+"]", "")
	}
	return changes
}

func normalizedActor(actor string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "unknown"
	}
	return actor
}

func (s *Store) AddWorkLog(input WorkLogInput, mutation Mutation) (WorkLogEntry, error) {
	entry, _, err := s.AddWorkLogResult(input, mutation)
	return entry, err
}

func (s *Store) AddWorkLogResult(input WorkLogInput, mutation Mutation) (WorkLogEntry, bool, error) {
	if mutation.TaskID == "" {
		mutation.TaskID = input.TaskID
	}
	var entry WorkLogEntry
	guard := mutation
	guard.ExpectedRevision = nil
	err := s.transactWithHistory(func(tasks map[string]Task, history *[]HistoryRecord) error {
		updated, created, err := s.appendWorkLog(tasks, *history, input, mutation)
		entry = created
		if err != nil {
			return err
		}
		if err := checkRevision(tasks, mutation.TaskID, mutation.ExpectedRevision); err != nil {
			return err
		}
		*history = updated
		entry = created
		return nil
	}, guard)
	if errors.Is(err, errAlreadyApplied) {
		return entry, true, nil
	}
	return entry, false, err
}

func (s *Store) appendWorkLog(tasks map[string]Task, history []HistoryRecord, input WorkLogInput, mutation Mutation) ([]HistoryRecord, WorkLogEntry, error) {
	if input.ActionID != "" {
		id, err := validateText("actionId", input.ActionID, 128, true)
		if err != nil || id != input.ActionID {
			return history, WorkLogEntry{}, errors.New("invalid actionId")
		}
		if input.actionHash == "" {
			normalized := input
			normalized.ActionID = ""
			normalized.Text = strings.TrimSpace(normalized.Text)
			hash, err := actionDigest(normalized)
			if err != nil {
				return history, WorkLogEntry{}, err
			}
			input.actionHash = hash
		}
		prior, err := findAction(history, input.TaskID, input.ActionID, input.actionHash)
		if err != nil {
			return history, WorkLogEntry{}, err
		}
		if prior != nil {
			return history, *prior, errAlreadyApplied
		}
	}
	task, ok := tasks[input.TaskID]
	if !ok {
		return history, WorkLogEntry{}, ErrNotFound
	}
	if input.SubtaskID != "" {
		found := false
		for _, sub := range task.Subtasks {
			if sub.ID == input.SubtaskID {
				found = true
				break
			}
		}
		if !found {
			return history, WorkLogEntry{}, ErrNotFound
		}
	}
	if !IsWorkLogKind(input.Kind) {
		return history, WorkLogEntry{}, errors.New("invalid work log kind")
	}
	if input.Kind == WorkLogReviewDelivered && (task.Kind != "review" || input.NeedsReview) {
		return history, WorkLogEntry{}, errors.New("review_delivered requires a review card and needsReview false")
	}
	text, err := validateText("work log text", input.Text, MaxWorkLogText, true)
	if err != nil {
		return history, WorkLogEntry{}, err
	}
	if input.OccurredAt.IsZero() {
		input.OccurredAt = s.now()
	}
	if input.OccurredAt.Location() == nil {
		return history, WorkLogEntry{}, errors.New("invalid occurrence time")
	}
	if input.Origin == "" {
		input.Origin = "direct"
	}
	if input.CorrectsID != "" && !hasHistory(history, input.CorrectsID) {
		return history, WorkLogEntry{}, fmt.Errorf("correction target: %w", ErrNotFound)
	}
	if input.ResolvesBlockerID != "" && !hasBlocker(history, input.ResolvesBlockerID) {
		return history, WorkLogEntry{}, fmt.Errorf("blocker target: %w", ErrNotFound)
	}
	id, err := s.randomID(12)
	if err != nil {
		return history, WorkLogEntry{}, err
	}
	entry := WorkLogEntry{ID: "w" + id, TaskID: input.TaskID, SubtaskID: input.SubtaskID, Kind: input.Kind, Text: text, Actor: normalizedActor(mutation.Actor), OccurredAt: input.OccurredAt.UTC(), RecordedAt: s.now(), SessionID: input.SessionID, Origin: input.Origin, CorrectsID: input.CorrectsID, ResolvesBlockerID: input.ResolvesBlockerID, NeedsReview: input.NeedsReview}
	entry.ActionID = input.ActionID
	entry.ActionHash = input.actionHash
	history = append(history, HistoryRecord{ID: entry.ID, TaskID: entry.TaskID, SubtaskID: entry.SubtaskID, At: entry.RecordedAt, Actor: entry.Actor, Kind: "work_log", Title: task.Title, WorkLog: &entry})
	return history, entry, nil
}

func hasHistory(history []HistoryRecord, id string) bool {
	for _, item := range history {
		if item.ID == id {
			return true
		}
	}
	return false
}
func hasBlocker(history []HistoryRecord, id string) bool {
	for _, item := range history {
		if item.ID == id && item.WorkLog != nil && item.WorkLog.Kind == WorkLogBlocker {
			return true
		}
	}
	return false
}

func (s *Store) History(filter HistoryFilter) HistoryPage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := filter.Limit
	if limit == 0 {
		limit = 50
	}
	if limit > maxHistoryPage {
		limit = maxHistoryPage
	}
	result := HistoryPage{PartialLegacyHistory: s.partialLegacyHistory}
	afterCursor := filter.Before == ""
	for i := len(s.history) - 1; i >= 0; i-- {
		item := s.history[i]
		if !afterCursor {
			if item.ID == filter.Before {
				afterCursor = true
			}
			continue
		}
		if filter.TaskID != "" && item.TaskID != filter.TaskID {
			continue
		}
		result.Records = append(result.Records, cloneHistory(item))
		if len(result.Records) == limit {
			result.NextBefore = item.ID
			break
		}
	}
	return result
}

func cloneHistory(item HistoryRecord) HistoryRecord {
	item.Changes = append([]FieldChange{}, item.Changes...)
	if item.WorkLog != nil {
		copy := *item.WorkLog
		item.WorkLog = &copy
	}
	return item
}

func ChicagoDateRange(start, end string) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	a, err := time.ParseInLocation("2006-01-02", start, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start date: %w", err)
	}
	b, err := time.ParseInLocation("2006-01-02", end, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid end date: %w", err)
	}
	if b.Before(a) {
		return time.Time{}, time.Time{}, errors.New("end date precedes start date")
	}
	return a.UTC(), b.AddDate(0, 0, 1).UTC(), nil
}
