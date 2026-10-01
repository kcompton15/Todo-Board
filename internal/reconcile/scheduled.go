package reconcile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

type Notifier interface {
	Notify(ctx context.Context, title, body string) error
}

type Scheduler struct {
	Runner    *Runner
	Location  *time.Location
	Now       func() time.Time
	Notifier  Notifier
	StatePath string
	NoLLM     bool
}

type TickResult struct {
	RunID         string         `json:"runId,omitempty"`
	At            time.Time      `json:"at"`
	Ran           bool           `json:"ran"`
	Skip          string         `json:"skip,omitempty"`
	Slot          string         `json:"slot,omitempty"`
	Attempt       int            `json:"attempt,omitempty"`
	Healthy       bool           `json:"healthy"`
	Error         string         `json:"error,omitempty"`
	Changed       int            `json:"changed"`
	Replayed      int            `json:"replayed"`
	Conflicted    int            `json:"conflicted"`
	Unavailable   int            `json:"unavailable"`
	Gaps          []EvidenceGap  `json:"gaps,omitempty"`
	Cursor        bool           `json:"cursorAdvanced"`
	Notifications []Notification `json:"notifications,omitempty"`
	NotifyErrors  []string       `json:"notifyErrors,omitempty"`
}

func (s *Scheduler) Tick(ctx context.Context) (TickResult, error) {
	now := s.Now()
	result := TickResult{At: now}
	unlock, err := Lock(LockPath(s.StatePath, s.Runner.Board.Origin))
	if errors.Is(err, ErrLocked) {
		result.Skip = "lock held"
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer unlock()
	if _, ok := DueSlot(now, s.Location); !ok {
		result.Skip = "outside schedule"
		return result, nil
	}
	state, err := LoadState(s.StatePath, s.Runner.Board.Origin)
	if err != nil {
		return result, err
	}
	schedule := ScheduleState{}
	if state.Schedule != nil {
		schedule = *state.Schedule
	}
	slot, skip, run := schedule.Decide(now, s.Location)
	if !run {
		result.Skip = skip
		return result, nil
	}
	result.Ran, result.Slot, result.RunID = true, slotKey(slot), newRunID()
	runResult, runErr := s.Runner.run(ctx, RunOptions{StatePath: s.StatePath, NoLLM: s.NoLLM})
	outcome := Outcome{RunID: result.RunID, Changed: runResult.ChangedCards, RequiredGaps: map[string]string{}}
	switch {
	case runErr != nil:
		outcome.Error = runErr.Error()
	case len(runResult.Errors) > 0:
		outcome.Error = runResult.Errors[0]
	case runResult.Unavailable > 0:
		outcome.Error = "required evidence unavailable"
	case runResult.Conflicted > 0:
		outcome.Error = "cards changed during the run"
	default:
		outcome.Healthy = true
	}
	for _, gap := range runResult.Gaps {
		if gap.Absent && gap.Required {
			outcome.RequiredGaps[gap.Identity] = gap.Reason
		}
	}
	// The run may have advanced the cursor; reload so schedule fields layer onto it.
	state, err = LoadState(s.StatePath, s.Runner.Board.Origin)
	if err != nil {
		return result, err
	}
	notes := schedule.Record(s.Now(), slot, s.Location, outcome)
	state.Schedule = &schedule
	if err := SaveState(s.StatePath, state); err != nil {
		return result, err
	}
	result.Attempt, result.Healthy, result.Error = schedule.Attempts, outcome.Healthy, outcome.Error
	result.Changed, result.Replayed, result.Conflicted, result.Unavailable = runResult.Changed, runResult.Replayed, runResult.Conflicted, runResult.Unavailable
	result.Gaps, result.Cursor, result.Notifications = runResult.Gaps, runResult.CursorAdvanced, notes
	for _, note := range notes {
		if s.Notifier == nil {
			continue
		}
		if err := s.Notifier.Notify(ctx, note.Title, note.Body); err != nil {
			result.NotifyErrors = append(result.NotifyErrors, err.Error())
		}
	}
	return result, nil
}

func newRunID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strings.ReplaceAll(time.Now().UTC().Format("150405.000000"), ".", "")
	}
	return hex.EncodeToString(b[:])
}
