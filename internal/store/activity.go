package store

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const MaxActivity = 1000

var ErrConflict = errors.New("revision conflict")
var ErrOpenSubtasks = errors.New("unfinished subtasks")

type Mutation struct {
	Actor            string
	TaskID           string
	ExpectedRevision *uint64
}

type FieldChange struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type Activity struct {
	TaskID   string        `json:"taskId"`
	Title    string        `json:"title"`
	At       time.Time     `json:"at"`
	Actor    string        `json:"actor"`
	Action   string        `json:"action"`
	Revision uint64        `json:"revision"`
	Changes  []FieldChange `json:"changes"`
}

func (s *Store) Activity(taskID string) []Activity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Activity, 0)
	for i := len(s.activity) - 1; i >= 0; i-- {
		event := s.activity[i]
		if taskID == "" || event.TaskID == taskID {
			event.Changes = append([]FieldChange{}, event.Changes...)
			result = append(result, event)
		}
	}
	return result
}

func OpenSubtasks(task Task) int {
	count := 0
	for _, sub := range task.Subtasks {
		if sub.Lane != "done" {
			count++
		}
	}
	return count
}

func checkRevision(tasks map[string]Task, id string, expected *uint64) error {
	if expected == nil {
		return nil
	}
	task, ok := tasks[id]
	if !ok {
		return ErrNotFound
	}
	if *expected != task.Revision {
		return fmt.Errorf("%w: task %s changed (expected %d, current %d); read the latest task before retrying", ErrConflict, id, *expected, task.Revision)
	}
	return nil
}

func preview(value string) string {
	runes := []rune(value)
	if len(runes) > 160 {
		return string(runes[:160]) + "…"
	}
	return value
}

func taskChanges(before, after Task) []FieldChange {
	changes := make([]FieldChange, 0)
	add := func(field, old, next string) {
		if old != next {
			changes = append(changes, FieldChange{preview(field), preview(old), preview(next)})
		}
	}
	add("title", before.Title, after.Title)
	add("lane", before.Lane, after.Lane)
	add("kind", before.Kind, after.Kind)
	add("priority", strconv.Itoa(before.Priority), strconv.Itoa(after.Priority))
	add("project", before.Project, after.Project)
	add("tag", before.Tag, after.Tag)
	add("notes", before.Notes, after.Notes)
	add("links", linksJSON(before.Links), linksJSON(after.Links))
	add("agentContext", before.AgentContext, after.AgentContext)
	add("reconcileMode", before.ReconcileMode, after.ReconcileMode)
	add("source", before.Source, after.Source)
	add("rank", strconv.FormatFloat(before.Order, 'f', -1, 64), strconv.FormatFloat(after.Order, 'f', -1, 64))
	old := make(map[string]Subtask)
	for _, sub := range before.Subtasks {
		old[sub.ID] = sub
	}
	for _, sub := range after.Subtasks {
		previous, exists := old[sub.ID]
		field := "subtask " + sub.ID
		if !exists {
			add(field, "", sub.Title+" ["+sub.Lane+"]")
		} else {
			add(field+" title", previous.Title, sub.Title)
			add(field+" lane", previous.Lane, sub.Lane)
		}
		delete(old, sub.ID)
	}
	for _, sub := range before.Subtasks {
		if _, exists := old[sub.ID]; exists {
			add("subtask "+sub.ID, sub.Title+" ["+sub.Lane+"]", "")
		}
	}
	return changes
}

func (s *Store) prepareChanges(candidate map[string]Task, actor string) ([]Activity, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "unknown"
	}
	if _, err := validateText("actor", actor, 80, true); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(candidate)+len(s.tasks))
	for id := range candidate {
		ids = append(ids, id)
	}
	for id := range s.tasks {
		if _, ok := candidate[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	events := append([]Activity{}, s.activity...)
	for _, id := range ids {
		before, existed := s.tasks[id]
		after, exists := candidate[id]
		if existed && exists && reflect.DeepEqual(before, after) {
			continue
		}
		if exists && after.Lane == "done" && OpenSubtasks(after) > 0 {
			// Legacy inconsistent cards remain editable; new open children under Done do not.
			allowed := existed && before.Lane == "done"
			oldOpen := make(map[string]bool)
			for _, sub := range before.Subtasks {
				oldOpen[sub.ID] = sub.Lane != "done"
			}
			for _, sub := range after.Subtasks {
				if sub.Lane != "done" && !oldOpen[sub.ID] {
					allowed = false
				}
			}
			if !allowed {
				return nil, fmt.Errorf("%w: %s has %d open steps; finish the steps or reopen the parent first", ErrOpenSubtasks, after.Title, OpenSubtasks(after))
			}
		}
		event := Activity{TaskID: id, At: s.now(), Actor: actor, Action: "updated", Title: after.Title, Changes: taskChanges(before, after)}
		if !existed {
			event.Action = "created"
		}
		if !exists {
			event.Action = "deleted"
			event.Title = before.Title
		}
		if before.Revision == ^uint64(0) {
			return nil, errors.New("task revision exhausted")
		}
		event.Revision = before.Revision + 1
		if exists {
			after.Revision = event.Revision
			candidate[id] = after
		}
		events = append(events, event)
	}
	if len(events) > MaxActivity {
		events = events[len(events)-MaxActivity:]
	}
	return events, nil
}
