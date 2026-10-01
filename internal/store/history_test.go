package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkLogIsDurableFullTextAndLinked(t *testing.T) {
	s := newTestStore(t)
	task, err := s.CreateMany([]TaskInput{{Title: "History QA"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("important evidence ", 40)
	blocker, err := s.AddWorkLog(WorkLogInput{TaskID: task[0].ID, Kind: WorkLogBlocker, Text: long, OccurredAt: time.Date(2026, 9, 21, 9, 0, 0, 0, time.FixedZone("CDT", -5))}, Mutation{Actor: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := s.AddWorkLog(WorkLogInput{TaskID: task[0].ID, Kind: WorkLogBlockerResolved, Text: "unblocked", ResolvesBlockerID: blocker.ID}, Mutation{Actor: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ResolvesBlockerID != blocker.ID {
		t.Fatalf("resolution lost link: %#v", resolved)
	}
	page := s.History(HistoryFilter{TaskID: task[0].ID})
	if len(page.Records) != 3 || page.Records[0].WorkLog == nil || page.Records[1].WorkLog == nil {
		t.Fatalf("unexpected history: %#v", page)
	}
	if page.Records[1].WorkLog.Text != strings.TrimSpace(long) {
		t.Fatalf("work log was truncated: %d", len(page.Records[1].WorkLog.Text))
	}
	if _, err := s.AddWorkLog(WorkLogInput{TaskID: task[0].ID, Kind: WorkLogDecision, Text: "correction", CorrectsID: blocker.ID}, Mutation{Actor: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(task[0].ID, Mutation{Actor: "codex"}); err != nil {
		t.Fatal(err)
	}
	if got := s.History(HistoryFilter{TaskID: task[0].ID}); len(got.Records) != 5 {
		t.Fatalf("deleted-task history lost: %#v", got)
	}
	first := s.History(HistoryFilter{TaskID: task[0].ID, Limit: 2})
	second := s.History(HistoryFilter{TaskID: task[0].ID, Limit: 2, Before: first.NextBefore})
	if len(first.Records) != 2 || len(second.Records) != 2 || first.Records[1].ID == second.Records[0].ID {
		t.Fatalf("cursor did not page in stored order: %#v %#v", first, second)
	}
}

func TestVersionOneMigrationBacksUpBeforeFirstWrite(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"version":1,"tasks":[{"id":"tlegacy","title":"Legacy","lane":"today","priority":2,"source":"web","order":1000,"createdAt":"2026-09-21T00:00:00Z","updatedAt":"2026-09-21T00:00:00Z","subtasks":[]}]}`
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s.History(HistoryFilter{}).PartialLegacyHistory {
		t.Fatal("v1 history must be marked partial")
	}
	if _, err := s.AddWorkLog(WorkLogInput{TaskID: "tlegacy", Kind: WorkLogProgress, Text: "capture begins"}, Mutation{Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "tasks.json.v1-") && strings.HasSuffix(entry.Name(), ".backup") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected v1 backup before migration write")
	}
	data, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": 2`) {
		t.Fatalf("migration did not persist v2: %s", data)
	}
}

func TestChicagoDateRangeIncludesDSTCalendarDays(t *testing.T) {
	start, end, err := ChicagoDateRange("2026-03-08", "2026-03-08")
	if err != nil {
		t.Fatal(err)
	}
	if got := end.Sub(start); got != 23*time.Hour {
		t.Fatalf("DST day should be 23 hours, got %s", got)
	}
}

func TestPreviewReportUsesRecordedEvidenceAndCarryForward(t *testing.T) {
	s := newTestStore(t)
	task, err := s.CreateMany([]TaskInput{{Title: "Report task", Project: "Internal Tools"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddWorkLog(WorkLogInput{TaskID: task[0].ID, Kind: WorkLogProgress, Text: "implemented durable history", OccurredAt: time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)}, Mutation{Actor: "codex"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddWorkLog(WorkLogInput{TaskID: task[0].ID, Kind: WorkLogBlocker, Text: "waiting for a decision", OccurredAt: time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)}, Mutation{Actor: "codex"}); err != nil {
		t.Fatal(err)
	}
	report, err := s.PreviewReport(ReportRequest{Kind: "weekly", Start: "2026-09-21", End: "2026-09-21", Projects: []string{"Internal Tools"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report.Markdown, "implemented durable history") || !strings.Contains(report.Markdown, "Current carry-forward context") || !strings.Contains(report.Markdown, "waiting for a decision") {
		t.Fatalf("unexpected report: %s", report.Markdown)
	}
}
