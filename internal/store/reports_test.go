package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var reportDay = time.Date(2026, time.September, 17, 15, 0, 0, 0, time.UTC)

func addLog(t *testing.T, s *Store, taskID string, kind WorkLogKind, text string, at time.Time) WorkLogEntry {
	t.Helper()
	entry, err := s.AddWorkLog(WorkLogInput{TaskID: taskID, Kind: kind, Text: text, OccurredAt: at}, Mutation{Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func weeklyEvidence(t *testing.T, s *Store, projects ...string) Evidence {
	t.Helper()
	evidence, err := s.ReportEvidence(ReportRequest{Kind: "weekly", Start: "2026-09-17", End: "2026-09-17", Projects: projects})
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func findCard(t *testing.T, evidence Evidence, number int) EvidenceCard {
	t.Helper()
	for _, card := range evidence.Cards {
		if card.Number == number {
			return card
		}
	}
	t.Fatalf("card #%d missing from evidence: %+v", number, evidence.Cards)
	return EvidenceCard{}
}

func TestReportEvidenceNormalizesReconcilerText(t *testing.T) {
	s := newTestStore(t)
	tasks, err := s.CreateMany([]TaskInput{{Title: "Release 13: land ABC-931, then ABC-871", Project: "Billing", Lane: "doing"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	id := tasks[0].ID
	addLog(t, s, id, WorkLogProgress, "observed ABC-931 QA\nobserved team/api!175 merged\nobserved team/web!126 merged\nobserved ABC-931 QA", reportDay)
	addLog(t, s, id, WorkLogProgress, "discovered reference: ABC-871 scope=sg44wbha\ndiscovered reference: team/web!131 scope=\nobserved ABC-931 Done\nobserved ABC-871 Code Review\nobserved ABC-871 Code Review", reportDay.Add(time.Minute))
	addLog(t, s, id, WorkLogProgress, "observed team/api!190 merged\nmerged team/web!140; merged team/api!190", reportDay.Add(2*time.Minute))
	addLog(t, s, id, WorkLogProgress, "871 pushed; Draft MRs api !182 + web !131 opened", reportDay.Add(3*time.Minute))
	addLog(t, s, id, WorkLogProgress, "871 pushed; Draft MRs api !182 + web !131 opened", reportDay.Add(4*time.Minute))
	addLog(t, s, id, WorkLogProgress, "discovered reference: ABC-871 scope=s2tunupi", reportDay.Add(5*time.Minute))

	card := findCard(t, weeklyEvidence(t, s), tasks[0].Number)
	wantObserved := []string{"ABC-931 Done", "api!175 merged", "web!126 merged", "ABC-871 Code Review", "api!190 merged", "web!140 merged"}
	if strings.Join(card.Observed, "|") != strings.Join(wantObserved, "|") {
		t.Fatalf("observed = %q, want %q", card.Observed, wantObserved)
	}
	if len(card.Progress) != 1 || card.Progress[0].Text != "871 pushed; Draft MRs api !182 + web !131 opened" {
		t.Fatalf("progress = %+v", card.Progress)
	}
	digest := RenderDigest(weeklyEvidence(t, s))
	if strings.Contains(digest, "discovered reference") || strings.Count(digest, "Observed:") != 1 || strings.Contains(digest, "team/") {
		t.Fatalf("digest kept reconciler noise:\n%s", digest)
	}
	if !strings.Contains(digest, "## Billing\n- #1 Release 13: land ABC-931, then ABC-871\n  - 871 pushed") {
		t.Fatalf("digest not grouped by project and card:\n%s", digest)
	}
}

func TestReportEvidenceGroupsCardsAndFiltersProjects(t *testing.T) {
	s := newTestStore(t)
	tasks, err := s.CreateMany([]TaskInput{
		{Title: "Tools card", Project: "Tools"},
		{Title: "Billing card", Project: "Billing"},
		{Title: "Loose card"},
		{Title: "Investigate", Project: "Billing", Kind: "probe"},
		{Title: "Rank only", Project: "Billing"},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	addLog(t, s, tasks[0].ID, WorkLogProgress, "tools work", reportDay)
	addLog(t, s, tasks[1].ID, WorkLogDecision, "chose option b", reportDay)
	addLog(t, s, tasks[1].ID, WorkLogProgress, "billing work", reportDay.Add(time.Minute))
	addLog(t, s, tasks[2].ID, WorkLogNextStep, "loose next", reportDay)
	addLog(t, s, tasks[3].ID, WorkLogProgress, "probe finding", reportDay)
	if _, err := s.Patch(tasks[4].ID, TaskPatch{Tag: ptr("ops")}); err != nil {
		t.Fatal(err)
	}

	evidence := weeklyEvidence(t, s)
	got := make([]string, 0)
	for _, card := range evidence.Cards {
		got = append(got, card.Project+"/"+card.Title)
	}
	if strings.Join(got, ",") != "Billing/Billing card,Tools/Tools card,/Loose card" {
		t.Fatalf("cards = %v", got)
	}
	billing := findCard(t, evidence, tasks[1].Number)
	if len(billing.Decisions) != 1 || len(billing.Progress) != 1 {
		t.Fatalf("billing card entries = %+v", billing)
	}
	if strings.Contains(RenderDigest(evidence), "probe finding") {
		t.Fatalf("probe leaked:\n%s", RenderDigest(evidence))
	}
	sourceTasks := make(map[string]bool)
	for _, record := range s.history {
		for _, id := range evidence.SourceRecordIDs {
			if record.ID == id {
				sourceTasks[record.TaskID] = true
			}
		}
	}
	if len(sourceTasks) != 3 || sourceTasks[tasks[3].ID] || sourceTasks[tasks[4].ID] {
		t.Fatalf("source records cover the wrong cards: %v", sourceTasks)
	}

	filtered := weeklyEvidence(t, s, "Billing")
	if len(filtered.Cards) != 1 || filtered.Cards[0].Title != "Billing card" || strings.Join(filtered.Projects, ",") != "Billing" {
		t.Fatalf("project filter = %+v", filtered)
	}
}

func TestReportEvidenceDetectsDoneAndCheckedStepsInWindow(t *testing.T) {
	s := newTestStore(t)
	tasks, err := s.CreateMany([]TaskInput{
		{Title: "Ship it", Project: "Tools", Lane: "doing", Subtasks: SubtaskInputs{{Title: "write code"}, {Title: "deploy"}}},
		{Title: "Still going", Project: "Tools", Lane: "doing"},
		{Title: "Waiting", Project: "Tools", Lane: "blocked"},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	ship := tasks[0]
	if _, _, err := s.PatchSubtask(ship.ID, ship.Subtasks[0].ID, SubtaskPatch{Lane: ptr("done")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PatchSubtask(ship.ID, ship.Subtasks[1].ID, SubtaskPatch{Lane: ptr("done")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Patch(ship.ID, TaskPatch{Lane: ptr("done")}); err != nil {
		t.Fatal(err)
	}

	evidence := weeklyEvidence(t, s)
	card := findCard(t, evidence, ship.Number)
	if !card.DoneInWindow || card.Lane != "done" || strings.Join(card.StepsChecked, ",") != "write code,deploy" {
		t.Fatalf("done card = %+v", card)
	}
	if len(evidence.Cards) != 1 {
		t.Fatalf("created-only cards should not count as evidence: %+v", evidence.Cards)
	}
	if len(evidence.ActiveCards) != 2 || evidence.ActiveCards[0].Title != "Still going" || evidence.ActiveCards[1].Lane != "blocked" {
		t.Fatalf("active cards = %+v", evidence.ActiveCards)
	}
	if !strings.Contains(RenderDigest(evidence), "- #1 Ship it (done)\n  - Checked: write code; deploy") {
		t.Fatalf("digest:\n%s", RenderDigest(evidence))
	}

	standup, err := s.ReportEvidence(ReportRequest{Kind: "standup", Start: "2026-09-18", End: "2026-09-18"})
	if err != nil {
		t.Fatal(err)
	}
	if len(standup.Cards) != 0 || len(standup.ActiveCards) != 0 {
		t.Fatalf("out-of-window standup = %+v", standup)
	}
}

func TestReportEvidenceCarriesOpenBlockersForward(t *testing.T) {
	s := newTestStore(t)
	tasks, err := s.CreateMany([]TaskInput{{Title: "Blocked card", Lane: "blocked"}, {Title: "Finished card", Lane: "done"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	old := reportDay.AddDate(0, -1, 0)
	addLog(t, s, tasks[0].ID, WorkLogBlocker, "waiting on vendor", old)
	resolved := addLog(t, s, tasks[0].ID, WorkLogBlocker, "waiting on access", old)
	if _, err := s.AddWorkLog(WorkLogInput{TaskID: tasks[0].ID, Kind: WorkLogBlockerResolved, Text: "access granted", OccurredAt: old.Add(time.Hour), ResolvesBlockerID: resolved.ID}, Mutation{Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	addLog(t, s, tasks[1].ID, WorkLogBlocker, "stale blocker on a done card", old)

	evidence := weeklyEvidence(t, s)
	if len(evidence.OpenBlockers) != 1 || evidence.OpenBlockers[0].Text != "waiting on vendor" || evidence.OpenBlockers[0].Number != tasks[0].Number {
		t.Fatalf("open blockers = %+v", evidence.OpenBlockers)
	}
	if !strings.Contains(RenderDigest(evidence), "## Blockers\n- #1 Blocked card: waiting on vendor") {
		t.Fatalf("digest:\n%s", RenderDigest(evidence))
	}
}

func TestReportEvidenceCapsEntriesAndTruncatesOldestProgress(t *testing.T) {
	s := newTestStore(t)
	tasks, err := s.CreateMany([]TaskInput{{Title: "Busy card"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	addLog(t, s, tasks[0].ID, WorkLogProgress, strings.Repeat("x", 900), reportDay)
	card := findCard(t, weeklyEvidence(t, s), tasks[0].Number)
	if got := []rune(card.Progress[0].Text); len(got) != MaxEvidenceEntryText || !strings.HasSuffix(card.Progress[0].Text, "...") {
		t.Fatalf("entry not capped: %d runes", len(got))
	}

	evidence := Evidence{Kind: "weekly", Cards: []EvidenceCard{{Number: 1, Title: "Busy card"}}}
	for i := 0; i < 20; i++ {
		evidence.Cards[0].Progress = append(evidence.Cards[0].Progress, EvidenceNote{At: reportDay.Add(time.Duration(i) * time.Minute), Text: strings.Repeat("p", 100) + string(rune('a'+i))})
	}
	evidence.Cards[0].Decisions = []EvidenceNote{{At: reportDay.Add(-time.Hour), Text: "oldest but not progress"}}
	limit := 1500
	if err := fitEvidence(&evidence, limit); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	progress := evidence.Cards[0].Progress
	if len(data) > limit || !evidence.Truncated || len(progress) == 0 || len(progress) == 20 {
		t.Fatalf("size %d, truncated %v, kept %d progress", len(data), evidence.Truncated, len(progress))
	}
	if !strings.HasSuffix(progress[len(progress)-1].Text, "t") || len(evidence.Cards[0].Decisions) != 1 {
		t.Fatalf("dropped the wrong notes: %+v", evidence.Cards[0])
	}
	if !strings.Contains(RenderDigest(evidence), "left out") {
		t.Fatal("digest does not say it was truncated")
	}
}

func TestReportEvidenceRejectsBadRequests(t *testing.T) {
	s := newTestStore(t)
	for _, request := range []ReportRequest{
		{Kind: "monthly", Start: "2026-09-17", End: "2026-09-17"},
		{Kind: "weekly", Start: "2026-09-18", End: "2026-09-17"},
		{Kind: "weekly", Start: "yesterday", End: "2026-09-17"},
	} {
		if _, err := s.ReportEvidence(request); err == nil {
			t.Fatalf("accepted %+v", request)
		}
	}
}
