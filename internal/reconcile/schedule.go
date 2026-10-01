package reconcile

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type slotTime struct{ Hour, Minute int }

var Slots = []slotTime{{9, 20}, {11, 20}, {13, 20}, {15, 20}, {16, 45}, {18, 30}}

const (
	windowStartHour = 9
	windowEndHour   = 19
	MaxAttempts     = 3
)

var Backoff = []time.Duration{5 * time.Minute, 15 * time.Minute}

func workday(t time.Time) bool {
	return t.Weekday() != time.Saturday && t.Weekday() != time.Sunday
}

func DueSlot(now time.Time, loc *time.Location) (time.Time, bool) {
	local := now.In(loc)
	if !workday(local) || local.Hour() < windowStartHour || local.Hour() >= windowEndHour {
		return time.Time{}, false
	}
	var due time.Time
	found := false
	for _, s := range Slots {
		at := time.Date(local.Year(), local.Month(), local.Day(), s.Hour, s.Minute, 0, 0, loc)
		if !at.After(local) {
			due, found = at, true
		}
	}
	return due, found
}

func NextSlot(now time.Time, loc *time.Location) time.Time {
	local := now.In(loc)
	for day := range 8 {
		date := time.Date(local.Year(), local.Month(), local.Day()+day, 0, 0, 0, 0, loc)
		if !workday(date) {
			continue
		}
		for _, s := range Slots {
			at := time.Date(date.Year(), date.Month(), date.Day(), s.Hour, s.Minute, 0, 0, loc)
			if at.After(local) {
				return at
			}
		}
	}
	return time.Time{}
}

func slotDeadline(slot time.Time, loc *time.Location) time.Time {
	local := slot.In(loc)
	end := time.Date(local.Year(), local.Month(), local.Day(), windowEndHour, 0, 0, 0, loc)
	for _, s := range Slots {
		at := time.Date(local.Year(), local.Month(), local.Day(), s.Hour, s.Minute, 0, 0, loc)
		if at.After(local) && at.Before(end) {
			return at
		}
	}
	return end
}

type GapRecord struct {
	Reason    string    `json:"reason"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
}

type ScheduleState struct {
	Slot             string               `json:"slot,omitempty"`
	Attempts         int                  `json:"attempts,omitempty"`
	SlotDone         bool                 `json:"slotDone,omitempty"`
	NextAttemptAt    *time.Time           `json:"nextAttemptAt,omitempty"`
	LastAttemptAt    *time.Time           `json:"lastAttemptAt,omitempty"`
	LastSuccessSlot  string               `json:"lastSuccessSlot,omitempty"`
	LastSuccessAt    *time.Time           `json:"lastSuccessAt,omitempty"`
	LastRunID        string               `json:"lastRunId,omitempty"`
	LastError        string               `json:"lastError,omitempty"`
	DegradedSince    *time.Time           `json:"degradedSince,omitempty"`
	DegradedNotified bool                 `json:"degradedNotified,omitempty"`
	Gaps             map[string]GapRecord `json:"gaps,omitempty"`
}

func slotKey(slot time.Time) string { return slot.UTC().Format(time.RFC3339) }

func (s ScheduleState) Decide(now time.Time, loc *time.Location) (time.Time, string, bool) {
	due, ok := DueSlot(now, loc)
	if !ok {
		return time.Time{}, "outside schedule", false
	}
	if s.Slot == slotKey(due) {
		switch {
		case s.SlotDone:
			return due, "slot complete", false
		case s.Attempts >= MaxAttempts:
			return due, "slot attempts exhausted", false
		case s.NextAttemptAt != nil && now.Before(*s.NextAttemptAt):
			return due, "retry backoff", false
		}
	}
	return due, "", true
}

type Outcome struct {
	RunID        string
	Healthy      bool
	Error        string
	Changed      []CardRef
	RequiredGaps map[string]string
}

type Notification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

const notifyTitle = "Board reconciler"

func (s *ScheduleState) Record(now, slot time.Time, loc *time.Location, outcome Outcome) []Notification {
	key := slotKey(slot)
	if s.Slot != key {
		s.Slot, s.Attempts, s.SlotDone, s.NextAttemptAt = key, 0, false, nil
	}
	s.Attempts++
	at := now
	s.LastAttemptAt = &at
	s.LastRunID = outcome.RunID
	notes := []Notification{}
	if len(outcome.Changed) > 0 {
		notes = append(notes, Notification{notifyTitle, changedSummary(outcome.Changed)})
	}
	if !outcome.Healthy {
		s.LastError = outcome.Error
		next := now.Add(Backoff[min(s.Attempts-1, len(Backoff)-1)])
		if s.Attempts < MaxAttempts && next.Before(slotDeadline(slot, loc)) {
			s.NextAttemptAt = &next
			return notes
		}
		s.Attempts = MaxAttempts
		s.NextAttemptAt = nil
		if s.DegradedSince == nil {
			s.DegradedSince = &at
		}
		if !s.DegradedNotified {
			s.DegradedNotified = true
			notes = append(notes, Notification{notifyTitle, fmt.Sprintf("Board sync failed %d times for the %s slot (%s). Giving it a rest until the next slot. Details: board-reconciler --status", MaxAttempts, slot.In(loc).Format("15:04"), outcome.Error)})
		}
		return notes
	}
	s.SlotDone, s.NextAttemptAt, s.LastError = true, nil, ""
	s.LastSuccessSlot = key
	s.LastSuccessAt = &at
	if s.DegradedSince != nil {
		if s.DegradedNotified {
			notes = append(notes, Notification{notifyTitle, "Board sync is healthy again."})
		}
		s.DegradedSince, s.DegradedNotified = nil, false
	}
	fresh := []string{}
	if s.Gaps == nil {
		s.Gaps = map[string]GapRecord{}
	}
	for identity := range s.Gaps {
		if _, ok := outcome.RequiredGaps[identity]; !ok {
			delete(s.Gaps, identity)
		}
	}
	for identity, reason := range outcome.RequiredGaps {
		record, known := s.Gaps[identity]
		if !known {
			record = GapRecord{Reason: reason, FirstSeen: now}
			fresh = append(fresh, identity)
		}
		record.LastSeen = now
		s.Gaps[identity] = record
	}
	if len(s.Gaps) == 0 {
		s.Gaps = nil
	}
	if len(fresh) > 0 {
		sort.Strings(fresh)
		notes = append(notes, Notification{notifyTitle, fmt.Sprintf("Can't read %s, so cards that require it stay open. Fix or remove the link. This is the only reminder.", strings.Join(fresh, ", "))})
	}
	return notes
}

func changedSummary(cards []CardRef) string {
	titles := []string{}
	for _, card := range cards {
		titles = append(titles, card.Title)
	}
	body := fmt.Sprintf("Updated %d card(s): %s", len(cards), strings.Join(titles, "; "))
	if runes := []rune(body); len(runes) > 240 {
		body = string(runes[:239]) + "…"
	}
	return body
}
