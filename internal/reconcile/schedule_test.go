package reconcile

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/kcompton15/Todo-Board/internal/httpapi"
	"github.com/kcompton15/Todo-Board/internal/store"
)

func chicago(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func at(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestDueSlotCalendar(t *testing.T) {
	loc := chicago(t)
	cases := []struct{ name, now, want string }{
		{"before first slot", "2026-10-05T09:19:59-05:00", ""},
		{"first slot", "2026-10-05T09:20:00-05:00", "09:20"},
		{"between slots keeps latest", "2026-10-05T11:19:00-05:00", "09:20"},
		{"late slot", "2026-10-05T16:45:00-05:00", "16:45"},
		{"last slot until window end", "2026-10-05T18:59:59-05:00", "18:30"},
		{"window end", "2026-10-05T19:00:00-05:00", ""},
		{"before window", "2026-10-05T08:59:00-05:00", ""},
		{"saturday", "2026-10-03T10:00:00-05:00", ""},
		{"sunday", "2026-10-04T18:30:00-05:00", ""},
		{"friday", "2026-10-09T18:30:00-05:00", "18:30"},
		{"last CDT weekday", "2026-10-30T14:20:00Z", "09:20"},
		{"first CST weekday", "2026-11-02T15:20:00Z", "09:20"},
		{"CST morning at old CDT offset", "2026-11-02T14:20:00Z", ""},
		{"last CST weekday", "2027-03-12T15:20:00Z", "09:20"},
		{"first CDT weekday", "2027-03-15T14:20:00Z", "09:20"},
		{"tokyo host clock", "2026-10-06T06:45:00+09:00", "16:45"},
		{"los angeles host clock", "2026-10-05T07:20:00-07:00", "09:20"},
		{"tokyo saturday is chicago friday", "2026-10-10T08:30:00+09:00", "18:30"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			due, ok := DueSlot(at(t, c.now), loc)
			got := ""
			if ok {
				got = due.In(loc).Format("15:04")
			}
			if got != c.want {
				t.Fatalf("DueSlot(%s) = %q, want %q", c.now, got, c.want)
			}
		})
	}
}

func TestNextSlotSkipsWeekend(t *testing.T) {
	loc := chicago(t)
	next := NextSlot(at(t, "2026-10-09T18:30:00-05:00"), loc)
	if want := at(t, "2026-10-12T09:20:00-05:00"); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

type clock struct{ now time.Time }

func (c *clock) step(t *testing.T, value string) time.Time {
	c.now = at(t, value)
	return c.now
}

func TestScheduleWakeCoalescesToLatestSlot(t *testing.T) {
	loc := chicago(t)
	s := ScheduleState{}
	slot, _, run := s.Decide(at(t, "2026-10-06T11:21:00-05:00"), loc)
	if !run {
		t.Fatal("11:20 slot should run")
	}
	s.Record(at(t, "2026-10-06T11:22:00-05:00"), slot, loc, Outcome{Healthy: true})
	// Asleep from 11:22 until 16:50: 13:20 and 15:20 are not replayed.
	slot, _, run = s.Decide(at(t, "2026-10-06T16:50:00-05:00"), loc)
	if !run || slot.In(loc).Format("15:04") != "16:45" {
		t.Fatalf("wake ran %v slot %s", run, slot)
	}
	s.Record(at(t, "2026-10-06T16:51:00-05:00"), slot, loc, Outcome{Healthy: true})
	if _, reason, run := s.Decide(at(t, "2026-10-06T16:52:00-05:00"), loc); run || reason != "slot complete" {
		t.Fatalf("second wake ran %v (%s)", run, reason)
	}
}

func TestScheduleNeverReplaysYesterday(t *testing.T) {
	loc := chicago(t)
	s := ScheduleState{}
	slot, _, _ := s.Decide(at(t, "2026-10-05T18:30:00-05:00"), loc)
	s.Record(at(t, "2026-10-05T18:31:00-05:00"), slot, loc, Outcome{Error: "board down"})
	for _, value := range []string{"2026-10-05T19:05:00-05:00", "2026-10-06T08:00:00-05:00", "2026-10-06T09:05:00-05:00"} {
		if _, reason, run := s.Decide(at(t, value), loc); run || reason != "outside schedule" {
			t.Fatalf("%s ran %v (%s)", value, run, reason)
		}
	}
	slot, _, run := s.Decide(at(t, "2026-10-06T09:25:00-05:00"), loc)
	if !run || !slot.Equal(at(t, "2026-10-06T09:20:00-05:00")) {
		t.Fatalf("tuesday slot %s run %v", slot, run)
	}
	s.Record(at(t, "2026-10-06T09:26:00-05:00"), slot, loc, Outcome{Healthy: true})
	if s.Attempts != 1 {
		t.Fatalf("new slot kept yesterday's attempts: %d", s.Attempts)
	}
}

func TestScheduleRetriesThenNotifiesOncePerEpisode(t *testing.T) {
	loc := chicago(t)
	s := ScheduleState{}
	c := &clock{}
	degraded := 0
	attempt := func(value string) (bool, []Notification) {
		slot, _, run := s.Decide(c.step(t, value), loc)
		if !run {
			return false, nil
		}
		notes := s.Record(c.now, slot, loc, Outcome{Error: "GitLab unreachable"})
		for _, n := range notes {
			if strings.Contains(n.Body, "failed 3 times") {
				degraded++
			}
		}
		return true, notes
	}
	for _, step := range []struct {
		at  string
		run bool
	}{
		{"2026-10-06T09:20:00-05:00", true},
		{"2026-10-06T09:24:59-05:00", false},
		{"2026-10-06T09:25:00-05:00", true},
		{"2026-10-06T09:39:00-05:00", false},
		{"2026-10-06T09:40:00-05:00", true},
		{"2026-10-06T09:41:00-05:00", false},
		{"2026-10-06T11:10:00-05:00", false},
		{"2026-10-06T11:20:00-05:00", true},
		{"2026-10-06T11:25:00-05:00", true},
		{"2026-10-06T11:40:00-05:00", true},
		{"2026-10-06T11:41:00-05:00", false},
	} {
		if ran, _ := attempt(step.at); ran != step.run {
			t.Fatalf("%s ran %v, want %v (state %+v)", step.at, ran, step.run, s)
		}
	}
	if degraded != 1 || s.DegradedSince == nil || !s.DegradedNotified {
		t.Fatalf("degraded notifications = %d, state %+v", degraded, s)
	}
	slot, _, _ := s.Decide(c.step(t, "2026-10-06T13:20:00-05:00"), loc)
	notes := s.Record(c.now, slot, loc, Outcome{Healthy: true})
	if len(notes) != 1 || !strings.Contains(notes[0].Body, "healthy again") || s.DegradedSince != nil {
		t.Fatalf("recovery notes %v state %+v", notes, s)
	}
}

func TestScheduleRetryStopsAtSlotDeadline(t *testing.T) {
	loc := chicago(t)
	cases := []struct{ first, second, lastSlot string }{
		{"2026-10-06T18:30:00-05:00", "2026-10-06T18:48:00-05:00", "window end"},
		{"2026-10-06T15:20:00-05:00", "2026-10-06T16:40:00-05:00", "next slot"},
	}
	for _, c := range cases {
		t.Run(c.lastSlot, func(t *testing.T) {
			s := ScheduleState{}
			slot, _, _ := s.Decide(at(t, c.first), loc)
			s.Record(at(t, c.first), slot, loc, Outcome{Error: "down"})
			if s.NextAttemptAt == nil {
				t.Fatal("first failure should schedule a retry")
			}
			notes := s.Record(at(t, c.second), slot, loc, Outcome{Error: "down"})
			if s.NextAttemptAt != nil || s.Attempts != MaxAttempts || len(notes) != 1 {
				t.Fatalf("retry past deadline scheduled: %+v %v", s, notes)
			}
		})
	}
}

func TestScheduleKnownGapNotifiesOncePerEpisode(t *testing.T) {
	loc := chicago(t)
	s := ScheduleState{}
	gap := map[string]string{"PROJ-842": "Jira issue missing"}
	run := func(value string, outcome Outcome) []Notification {
		slot, reason, ok := s.Decide(at(t, value), loc)
		if !ok {
			t.Fatalf("%s skipped: %s", value, reason)
		}
		return s.Record(at(t, value), slot, loc, outcome)
	}
	if notes := run("2026-10-06T09:20:00-05:00", Outcome{Healthy: true, RequiredGaps: gap}); len(notes) != 1 || !strings.Contains(notes[0].Body, "PROJ-842") {
		t.Fatalf("first sighting notes %v", notes)
	}
	if notes := run("2026-10-06T11:20:00-05:00", Outcome{Healthy: true, RequiredGaps: gap}); len(notes) != 0 {
		t.Fatalf("repeat gap notified: %v", notes)
	}
	if notes := run("2026-10-07T09:20:00-05:00", Outcome{Healthy: true, RequiredGaps: gap}); len(notes) != 0 || s.Attempts != 1 || !s.SlotDone {
		t.Fatalf("next day gap notified or retried: %v %+v", notes, s)
	}
	// A failed run cannot observe gaps, so it neither clears nor re-announces them.
	run("2026-10-07T11:20:00-05:00", Outcome{Error: "Jira fetch failed"})
	if _, ok := s.Gaps["PROJ-842"]; !ok {
		t.Fatal("failed run cleared a known gap")
	}
	run("2026-10-07T13:20:00-05:00", Outcome{Healthy: true})
	if s.Gaps != nil {
		t.Fatalf("resolved gap retained: %+v", s.Gaps)
	}
	if notes := run("2026-10-07T15:20:00-05:00", Outcome{Healthy: true, RequiredGaps: gap}); len(notes) != 1 {
		t.Fatalf("new episode not announced: %v", notes)
	}
}

type fakeJira struct {
	issues map[string]IssueState
	err    error
	calls  int
}

func (f *fakeJira) Issues(_ context.Context, keys []string) (map[string]IssueState, error) {
	f.calls++
	out := map[string]IssueState{}
	for _, key := range keys {
		if issue, ok := f.issues[key]; ok {
			out[key] = issue
		}
	}
	return out, f.err
}

type countingGitLab struct {
	noProvider
	calls int
}

func (g *countingGitLab) MR(ctx context.Context, project string, iid int) (MRState, error) {
	g.calls++
	return g.noProvider.MR(ctx, project, iid)
}
func (g *countingGitLab) SearchMRs(ctx context.Context, key string) ([]MRState, error) {
	g.calls++
	return g.noProvider.SearchMRs(ctx, key)
}

type recordingNotifier struct{ notes []Notification }

func (r *recordingNotifier) Notify(_ context.Context, title, body string) error {
	r.notes = append(r.notes, Notification{title, body})
	return nil
}

type harness struct {
	store     *store.Store
	scheduler *Scheduler
	clock     *clock
	notifier  *recordingNotifier
	jira      *fakeJira
	gitlab    *countingGitLab
}

func newHarness(t *testing.T, inputs []store.TaskInput) *harness {
	t.Helper()
	s, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateMany(inputs, ""); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(s, httpapi.NewHub(), nil, nil).Handler())
	t.Cleanup(server.Close)
	board, err := NewBoard(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{store: s, clock: &clock{}, notifier: &recordingNotifier{}, jira: &fakeJira{issues: map[string]IssueState{"PROJ-1": {Key: "PROJ-1", Status: "In Progress", Category: "indeterminate"}}}, gitlab: &countingGitLab{}}
	runner := &Runner{Board: board, GitLab: h.gitlab, Jira: h.jira, IssuePrefixes: []string{"PROJ"}}
	h.scheduler = &Scheduler{Runner: runner, Location: chicago(t), Now: func() time.Time { return h.clock.now }, Notifier: h.notifier, StatePath: filepath.Join(t.TempDir(), "state", "board-reconciler.json"), NoLLM: true}
	return h
}

func (h *harness) tick(t *testing.T, value string) TickResult {
	t.Helper()
	h.clock.step(t, value)
	result, err := h.scheduler.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func linked(title string, refs map[string]string) store.TaskInput {
	links := []store.LinkInput{}
	for ref, role := range refs {
		links = append(links, store.LinkInput{Ref: ref, Role: role})
	}
	return store.TaskInput{Title: title, Kind: "work", Links: &links}
}

func TestTickRequiredAbsentIdentityIsKnownGap(t *testing.T) {
	h := newHarness(t, []store.TaskInput{
		linked("gap card", map[string]string{"PROJ-842": "required"}),
		linked("visible card", map[string]string{"PROJ-1": "required"}),
	})
	first := h.tick(t, "2026-10-06T09:20:30-05:00")
	if !first.Ran || !first.Healthy || !first.Cursor || first.Unavailable != 0 {
		t.Fatalf("known gap failed the run: %+v", first)
	}
	if len(h.notifier.notes) != 1 || !strings.Contains(h.notifier.notes[0].Body, "PROJ-842") {
		t.Fatalf("gap notifications %v", h.notifier.notes)
	}
	if again := h.tick(t, "2026-10-06T09:21:30-05:00"); again.Ran || again.Skip != "slot complete" {
		t.Fatalf("completed slot reran: %+v", again)
	}
	next := h.tick(t, "2026-10-07T09:20:30-05:00")
	if !next.Ran || !next.Healthy || next.Attempt != 1 || len(h.notifier.notes) != 1 {
		t.Fatalf("next day retried or renotified: %+v %v", next, h.notifier.notes)
	}
	for _, task := range h.store.List(store.Filter{}) {
		if task.Lane == "done" {
			t.Fatalf("gap closed card %s", task.Title)
		}
	}
}

func TestTickReferenceOnlyGapIsSilent(t *testing.T) {
	h := newHarness(t, []store.TaskInput{{Title: "Backlog refinement PROJ-936", Kind: "work"}, linked("visible", map[string]string{"PROJ-1": "required"})})
	result := h.tick(t, "2026-10-06T09:20:30-05:00")
	if !result.Healthy || len(h.notifier.notes) != 0 {
		t.Fatalf("reference gap was loud: %+v %v", result, h.notifier.notes)
	}
	found := false
	for _, gap := range result.Gaps {
		if gap.Identity == "PROJ-936" && gap.Absent && !gap.Required {
			found = true
		}
	}
	if !found {
		t.Fatalf("reference gap not reported: %+v", result.Gaps)
	}
}

func TestTickAllKeysMissingIsProviderFailure(t *testing.T) {
	h := newHarness(t, []store.TaskInput{linked("gap card", map[string]string{"PROJ-842": "required"})})
	result := h.tick(t, "2026-10-06T09:20:30-05:00")
	if result.Healthy || result.Unavailable != 1 || result.Cursor {
		t.Fatalf("empty Jira answer treated as a known gap: %+v", result)
	}
	if retry := h.tick(t, "2026-10-06T09:25:30-05:00"); !retry.Ran || retry.Attempt != 2 {
		t.Fatalf("no retry: %+v", retry)
	}
}

func TestTickGitLabOutageStillReadsJira(t *testing.T) {
	inputs := []store.TaskInput{linked("jira work", map[string]string{"PROJ-1": "required"})}
	for i := 1; i <= 6; i++ {
		inputs = append(inputs, linked("mr work", map[string]string{"team/api!" + string(rune('0'+i)): "required"}))
	}
	h := newHarness(t, inputs)
	result := h.tick(t, "2026-10-06T09:20:30-05:00")
	if h.gitlab.calls != 3 || h.jira.calls != 1 {
		t.Fatalf("gitlab calls %d jira calls %d", h.gitlab.calls, h.jira.calls)
	}
	if result.Healthy || result.Unavailable != 6 {
		t.Fatalf("outage should be a retryable failure: %+v", result)
	}
}

func TestTickSkipsWithoutStateOrNetwork(t *testing.T) {
	h := newHarness(t, []store.TaskInput{{Title: "idle", Kind: "work"}})
	for _, value := range []string{"2026-10-10T10:00:00-05:00", "2026-10-06T08:59:00-05:00"} {
		if result := h.tick(t, value); result.Ran || result.Skip != "outside schedule" {
			t.Fatalf("%s: %+v", value, result)
		}
	}
	if _, err := os.Stat(h.scheduler.StatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("skip wrote state")
	}
	unlock, err := Lock(LockPath(h.scheduler.StatePath, h.scheduler.Runner.Board.Origin))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if result := h.tick(t, "2026-10-06T09:20:30-05:00"); result.Ran || result.Skip != "lock held" {
		t.Fatalf("ran while locked: %+v", result)
	}
	if _, err := h.scheduler.Runner.Run(context.Background(), RunOptions{DryRun: true, NoLLM: true, StatePath: h.scheduler.StatePath}); !errors.Is(err, ErrLocked) {
		t.Fatalf("manual run ignored scheduled lock: %v", err)
	}
}

func TestTickSummarizesCompletionsAndStaysQuietOnNoOps(t *testing.T) {
	h := newHarness(t, []store.TaskInput{linked("shipped", map[string]string{"PROJ-2": "required"}), linked("in flight", map[string]string{"PROJ-1": "required"})})
	h.jira.issues["PROJ-2"] = IssueState{Key: "PROJ-2", Status: "Done", Category: "done"}
	first := h.tick(t, "2026-10-06T09:20:30-05:00")
	if !first.Healthy || len(h.notifier.notes) != 1 || h.notifier.notes[0].Body != "Updated 1 card(s): shipped" {
		t.Fatalf("completion summary %+v %v", first, h.notifier.notes)
	}
	second := h.tick(t, "2026-10-06T11:20:30-05:00")
	if !second.Healthy || second.Changed != 0 || len(h.notifier.notes) != 1 {
		t.Fatalf("healthy no-op was loud: %+v %v", second, h.notifier.notes)
	}
}
