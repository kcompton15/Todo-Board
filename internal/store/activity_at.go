package store

import (
	"reflect"
	"strings"
	"time"
)

const reconcilerActor = "board-reconciler"

// stampActivity sets ActivityAt on created cards and on cards whose content a person or agent changed.
func stampActivity(before, after map[string]Task, actor string, now time.Time) {
	for id, next := range after {
		old, existed := before[id]
		switch {
		case !existed:
			if next.ActivityAt.IsZero() {
				next.ActivityAt = next.CreatedAt
			}
			if next.ActivityAt.IsZero() {
				next.ActivityAt = now
			}
		case !reflect.DeepEqual(activityView(old, actor), activityView(next, actor)):
			next.ActivityAt = now
		default:
			next.ActivityAt = old.ActivityAt
		}
		after[id] = next
	}
}

func activityView(task Task, actor string) Task {
	view := cloneTask(task)
	view.Order, view.Revision = 0, 0
	view.UpdatedAt, view.ActivityAt = time.Time{}, time.Time{}
	if strings.TrimSpace(actor) == reconcilerActor {
		view.Links, view.AgentContext = nil, ""
		return view
	}
	for i := range view.Links {
		view.Links[i].State, view.Links[i].StateCategory, view.Links[i].StateChangedAt = "", "", nil
	}
	return view
}

// backfillActivity fills ActivityAt for cards stored before the field existed.
func backfillActivity(tasks map[string]Task, history []HistoryRecord, completeSince time.Time) {
	latest := make(map[string]time.Time)
	for _, record := range history {
		if historyIsActivity(record) && record.At.After(latest[record.TaskID]) {
			latest[record.TaskID] = record.At
		}
	}
	for id, task := range tasks {
		if !task.ActivityAt.IsZero() {
			continue
		}
		switch {
		case !latest[id].IsZero():
			task.ActivityAt = latest[id]
		case !completeSince.IsZero() && !task.CreatedAt.Before(completeSince):
			task.ActivityAt = task.CreatedAt
		default:
			task.ActivityAt = task.UpdatedAt
		}
		tasks[id] = task
	}
}

func historyIsActivity(record HistoryRecord) bool {
	switch record.Kind {
	case "task_created":
		return true
	case "task_updated":
	default:
		return false
	}
	for _, change := range record.Changes {
		if change.Field == "rank" {
			continue
		}
		if record.Actor == reconcilerActor && (change.Field == "links" || change.Field == "agentContext") {
			continue
		}
		return true
	}
	return false
}
