package store

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	_ "time/tzdata" // Keep America/Chicago calendar reporting correct in the minimal Alpine container.
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

// PreviewReport renders only recorded evidence and makes no model call.
func (s *Store) PreviewReport(request ReportRequest) (ReportPreview, error) {
	if request.Kind != "standup" && request.Kind != "weekly" {
		return ReportPreview{}, errors.New("report kind must be standup or weekly")
	}
	start, end, err := ChicagoDateRange(request.Start, request.End)
	if err != nil {
		return ReportPreview{}, err
	}
	projects := make(map[string]bool)
	for _, project := range request.Projects {
		if trimmed := strings.TrimSpace(project); trimmed != "" {
			projects[trimmed] = true
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	selected := make([]HistoryRecord, 0)
	for _, record := range s.history {
		activityAt := record.At
		if record.WorkLog != nil {
			activityAt = record.WorkLog.OccurredAt
		}
		if activityAt.Before(start) || !activityAt.Before(end) {
			continue
		}
		if len(projects) > 0 {
			task, ok := s.tasks[record.TaskID]
			if !ok || !projects[task.Project] {
				continue
			}
		}
		selected = append(selected, cloneHistory(record))
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].At.Before(selected[j].At) })
	lines := []string{fmt.Sprintf("# %s (%s through %s)", map[bool]string{true: "Standup", false: "Weekly report"}[request.Kind == "standup"], request.Start, request.End)}
	sections := map[string][]string{"Progress": {}, "Decisions": {}, "Blockers": {}, "Blockers resolved": {}, "Next steps": {}}
	ids := make([]string, 0, len(selected))
	for _, record := range selected {
		ids = append(ids, record.ID)
		if record.WorkLog == nil {
			continue
		}
		entry := record.WorkLog
		title := record.Title
		if task, ok := s.tasks[entry.TaskID]; ok {
			title = task.Title
		}
		section := map[WorkLogKind]string{WorkLogReviewDelivered: "Progress", WorkLogProgress: "Progress", WorkLogDecision: "Decisions", WorkLogBlocker: "Blockers", WorkLogBlockerResolved: "Blockers resolved", WorkLogNextStep: "Next steps"}[entry.Kind]
		sections[section] = append(sections[section], "- "+title+": "+entry.Text+sourceSuffix(entry.NeedsReview))
	}
	for _, section := range []string{"Progress", "Decisions", "Blockers", "Blockers resolved", "Next steps"} {
		if len(sections[section]) > 0 {
			lines = append(lines, "", "## "+section)
			lines = append(lines, sections[section]...)
		}
	}
	if len(selected) == 0 {
		lines = append(lines, "", "No recorded work-log or task-change evidence falls in this period.")
	}
	if request.Kind == "weekly" {
		lines = append(lines, "", "## Current carry-forward context")
		for _, record := range s.history {
			if record.WorkLog != nil && record.WorkLog.Kind == WorkLogBlocker && !isResolved(s.history, record.WorkLog.ID) {
				lines = append(lines, "- "+taskTitle(s.tasks, record.TaskID, record.Title)+": "+record.WorkLog.Text)
			}
		}
	}
	return ReportPreview{Kind: request.Kind, Start: request.Start, End: request.End, Markdown: strings.Join(lines, "\n"), SourceRecordIDs: ids, PartialLegacyHistory: s.partialLegacyHistory}, nil
}

func sourceSuffix(needsReview bool) string {
	if needsReview {
		return " _(needs review)_"
	}
	return ""
}
func taskTitle(tasks map[string]Task, id, fallback string) string {
	if task, ok := tasks[id]; ok {
		return task.Title
	}
	return fallback
}
func isResolved(history []HistoryRecord, blockerID string) bool {
	for _, record := range history {
		if record.WorkLog != nil && record.WorkLog.ResolvesBlockerID == blockerID {
			return true
		}
	}
	return false
}
