package store

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const stateVersion = 2

type persistedState struct {
	Version              int             `json:"version"`
	Tasks                []Task          `json:"tasks"`
	Activity             []Activity      `json:"activity,omitempty"`
	History              []HistoryRecord `json:"history,omitempty"`
	CompleteHistorySince time.Time       `json:"completeHistorySince,omitempty"`
	PartialLegacyHistory bool            `json:"partialLegacyHistory,omitempty"`
}

type Store struct {
	activity             []Activity
	history              []HistoryRecord
	completeHistorySince time.Time
	partialLegacyHistory bool
	needsMigrationBackup bool
	mu                   sync.RWMutex
	path                 string
	tasks                map[string]Task
	now                  func() time.Time
	randomID             func(int) (string, error)
	afterPersist         func()
}

func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "inbox", ".processed"), 0o755); err != nil {
		return nil, fmt.Errorf("create processed inbox: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "inbox", ".failed"), 0o755); err != nil {
		return nil, fmt.Errorf("create failed inbox: %w", err)
	}

	s := &Store{
		path:     filepath.Join(dataDir, "tasks.json"),
		tasks:    make(map[string]Task),
		now:      func() time.Time { return time.Now().UTC() },
		randomID: randomBase36,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	if state.Version != 1 && state.Version != stateVersion {
		return fmt.Errorf("parse %s: unsupported state version %d", s.path, state.Version)
	}
	s.activity = state.Activity
	s.history = state.History
	s.completeHistorySince = state.CompleteHistorySince
	s.partialLegacyHistory = state.PartialLegacyHistory || state.Version == 1
	s.needsMigrationBackup = state.Version == 1
	for _, task := range state.Tasks {
		if task.Revision == 0 {
			task.Revision = 1
		}
		if task.ID == "" {
			return fmt.Errorf("parse %s: task has empty id", s.path)
		}
		if _, exists := s.tasks[task.ID]; exists {
			return fmt.Errorf("parse %s: duplicate task id %q", s.path, task.ID)
		}
		normalized, err := normalizeStoredTask(task)
		if err != nil {
			return fmt.Errorf("parse %s: task %s: %w", s.path, task.ID, err)
		}
		s.tasks[task.ID] = cloneTask(normalized)
	}
	return nil
}

func normalizeStoredTask(task Task) (Task, error) {
	input := TaskInput{
		Title: task.Title, Lane: task.Lane, Kind: task.Kind, Priority: task.Priority,
		Project: task.Project, Tag: task.Tag, Notes: task.Notes, Source: task.Source,
	}
	for _, subtask := range task.Subtasks {
		if subtask.ID == "" {
			return Task{}, errors.New("subtask has empty id")
		}
		input.Subtasks = append(input.Subtasks, SubtaskInput(subtask))
	}
	normalized, err := normalizeInput(input, task.Source)
	if err != nil {
		return Task{}, err
	}
	task.Kind = normalized.Kind
	if task.ReconcileMode == "" {
		task.ReconcileMode = "automatic"
	}
	if task.Links == nil {
		task.Links = []Link{}
	}
	if err := validateStoredMetadata(task); err != nil {
		return Task{}, err
	}
	for i := range task.Subtasks {
		task.Subtasks[i].Lane = normalized.Subtasks[i].Lane
		task.Subtasks[i].Done = normalized.Subtasks[i].Done
	}
	return task, nil
}

func (s *Store) List(filter Filter) []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return filterAndSort(s.tasks, filter)
}

func (s *Store) Get(id string) (Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, ok := s.tasks[id]
	if !ok {
		return Task{}, ErrNotFound
	}
	return cloneTask(task), nil
}

func (s *Store) CreateMany(inputs []TaskInput, sourceOverride string, options ...Mutation) ([]Task, error) {
	if len(inputs) == 0 {
		return nil, errors.New("at least one task is required")
	}
	if len(options) == 0 {
		options = []Mutation{{Actor: sourceOverride}}
	}
	var created []Task
	err := s.transact(func(tasks map[string]Task) error {
		created = make([]Task, 0, len(inputs))
		for i, input := range inputs {
			if sourceOverride != "" {
				input.Source = sourceOverride
			}
			task, err := s.createTask(tasks, input)
			if err != nil {
				return fmt.Errorf("tasks[%d]: %w", i, err)
			}
			created = append(created, task)
		}
		return nil
	}, options...)
	return cloneTasks(created), err
}

func (s *Store) Replace(id string, input TaskInput, options ...Mutation) (Task, error) {
	var result Task
	err := s.transact(func(tasks map[string]Task) error {
		current, ok := tasks[id]
		if !ok {
			return ErrNotFound
		}
		normalized, err := normalizeInput(input, defaultTaskSource)
		if err != nil {
			return err
		}
		order := current.Order
		if normalized.Order != nil {
			order = *normalized.Order
		} else if normalized.Lane != current.Lane {
			order = nextOrder(tasks, normalized.Lane, id)
		}
		subtasks, err := s.makeSubtasks(normalized.Subtasks, normalized.Lane)
		if err != nil {
			return err
		}
		result = Task{
			Links: current.Links, AgentContext: current.AgentContext, ReconcileMode: current.ReconcileMode,
			Revision: current.Revision + 1, ID: id, Title: normalized.Title, Lane: normalized.Lane, Priority: normalized.Priority,
			Kind: normalized.Kind, Project: normalized.Project, Tag: normalized.Tag, Notes: normalized.Notes,
			Source: normalized.Source, Order: order, CreatedAt: current.CreatedAt,
			UpdatedAt: s.now(), Subtasks: subtasks,
		}
		if err := s.metadata(&result, input.Links, input.AgentContext, input.ReconcileMode); err != nil {
			return err
		}
		tasks[id] = result
		return nil
	}, options...)
	return cloneTask(result), err
}

func (s *Store) Patch(id string, patch TaskPatch, options ...Mutation) (Task, error) {
	var result Task
	err := s.transact(func(tasks map[string]Task) error {
		current, ok := tasks[id]
		if !ok {
			return ErrNotFound
		}
		updated, err := s.applyPatch(tasks, current, patch)
		if err != nil {
			return err
		}
		tasks[id] = updated
		result = updated
		return nil
	}, options...)
	return cloneTask(result), err
}

func (s *Store) Delete(id string, options ...Mutation) error {
	return s.transact(func(tasks map[string]Task) error {
		if _, ok := tasks[id]; !ok {
			return ErrNotFound
		}
		delete(tasks, id)
		return nil
	}, options...)
}

func (s *Store) AddSubtasks(id string, inputs SubtaskInputs, options ...Mutation) ([]Subtask, uint64, error) {
	if len(inputs) == 0 {
		return nil, 0, errors.New("at least one subtask title is required")
	}
	var created []Subtask
	var committed map[string]Task
	err := s.transact(func(tasks map[string]Task) error {
		committed = tasks
		task, ok := tasks[id]
		if !ok {
			return ErrNotFound
		}
		if len(task.Subtasks)+len(inputs) > MaxSubtasks {
			return fmt.Errorf("task cannot contain more than %d subtasks", MaxSubtasks)
		}
		created = make([]Subtask, 0, len(inputs))
		for i, input := range inputs {
			normalized, err := normalizeSubtaskInput(input, task.Lane)
			if err != nil {
				return fmt.Errorf("subtasks[%d]: %w", i, err)
			}
			subtaskID, err := s.newSubtaskID()
			if err != nil {
				return err
			}
			subtask := Subtask{
				ID: subtaskID, Title: normalized.Title, Lane: normalized.Lane, Done: normalized.Done,
			}
			task.Subtasks = append(task.Subtasks, subtask)
			created = append(created, subtask)
		}
		task.UpdatedAt = s.now()
		tasks[task.ID] = task
		return nil
	}, options...)
	if err != nil {
		return nil, 0, err
	}
	return append([]Subtask(nil), created...), committed[id].Revision, nil
}

func (s *Store) PatchSubtask(id, subtaskID string, patch SubtaskPatch, options ...Mutation) (Subtask, uint64, error) {
	if patch.Title == nil && patch.Lane == nil && patch.Done == nil {
		return Subtask{}, 0, errors.New("at least one subtask field is required")
	}
	var result Subtask
	var committed map[string]Task
	err := s.transact(func(tasks map[string]Task) error {
		committed = tasks
		task, ok := tasks[id]
		if !ok {
			return ErrNotFound
		}
		index := -1
		for i := range task.Subtasks {
			if task.Subtasks[i].ID == subtaskID {
				index = i
				break
			}
		}
		if index < 0 {
			return ErrNotFound
		}
		if err := applySubtaskPatch(&task, index, patch); err != nil {
			return err
		}
		task.UpdatedAt = s.now()
		result = task.Subtasks[index]
		tasks[id] = task
		return nil
	}, options...)
	if err != nil {
		return Subtask{}, 0, err
	}
	return result, committed[id].Revision, nil
}

func applySubtaskPatch(task *Task, index int, patch SubtaskPatch) error {
	if patch.Title != nil {
		title, err := validateText("subtask title", *patch.Title, MaxSubtaskTitle, true)
		if err != nil {
			return err
		}
		task.Subtasks[index].Title = title
	}
	if patch.Lane != nil {
		if !IsLane(*patch.Lane) {
			return fmt.Errorf("invalid subtask lane %q", *patch.Lane)
		}
		task.Subtasks[index].Lane = *patch.Lane
		task.Subtasks[index].Done = *patch.Lane == "done"
		return nil
	}
	if patch.Done == nil {
		return nil
	}
	if *patch.Done {
		task.Subtasks[index].Lane = "done"
		task.Subtasks[index].Done = true
		return nil
	}
	if task.Subtasks[index].Lane == "done" {
		fallback := task.Lane
		if fallback == "done" {
			fallback = "today"
		}
		task.Subtasks[index].Lane = fallback
	}
	task.Subtasks[index].Done = false
	return nil
}

func (s *Store) DeleteSubtask(id, subtaskID string, options ...Mutation) error {
	return s.transact(func(tasks map[string]Task) error {
		task, ok := tasks[id]
		if !ok {
			return ErrNotFound
		}
		index := -1
		for i := range task.Subtasks {
			if task.Subtasks[i].ID == subtaskID {
				index = i
				break
			}
		}
		if index < 0 {
			return ErrNotFound
		}
		task.Subtasks = append(task.Subtasks[:index], task.Subtasks[index+1:]...)
		pruneLinks(&task)
		task.UpdatedAt = s.now()
		tasks[id] = task
		return nil
	}, options...)
}

func (s *Store) ApplyOps(operations []Operation, source string, options ...Mutation) ([]Task, error) {
	tasks, _, err := s.ApplyEnvelope(OpsEnvelope{Ops: operations, Source: source}, options...)
	return tasks, err
}

func (s *Store) ApplyEnvelope(envelope OpsEnvelope, options ...Mutation) ([]Task, bool, error) {
	operations, source := envelope.Ops, envelope.Source
	if len(options) == 0 {
		options = []Mutation{{Actor: source}}
	}
	if len(operations) == 0 {
		return nil, false, errors.New("ops must contain at least one operation")
	}
	var committed []Task
	err := s.transactWithHistory(func(tasks map[string]Task, history *[]HistoryRecord) error {
		hash, receipt, err := prepareAction(envelope, *history)
		if err != nil {
			return err
		}
		if receipt != nil {
			committed = filterAndSort(tasks, Filter{})
			return errAlreadyApplied
		}
		if err := checkHistoryHeads(*history, envelope.ExpectedHistoryHeads); err != nil {
			return err
		}
		for i, operation := range operations {
			if err := checkRevision(tasks, operation.ID, operation.ExpectedRevision); err != nil {
				return fmt.Errorf("ops[%d]: %w", i, err)
			}
		}
		for i, operation := range operations {
			if operation.Op == "work_log" {
				if operation.WorkLog == nil {
					return fmt.Errorf("ops[%d]: work_log operation requires workLog", i)
				}
				input := *operation.WorkLog
				if input.TaskID != "" && input.TaskID != operation.ID {
					return fmt.Errorf("ops[%d]: workLog taskId must match operation id", i)
				}
				input.TaskID = operation.ID
				if input.ActionID != "" {
					input.actionHash = hash
				}
				updated, _, err := s.appendWorkLog(tasks, *history, input, Mutation{Actor: options[0].Actor, TaskID: operation.ID})
				if err != nil {
					return fmt.Errorf("ops[%d]: %w", i, err)
				}
				*history = updated
				continue
			}
			if err := s.applyOperation(tasks, operation, source); err != nil {
				return fmt.Errorf("ops[%d]: %w", i, err)
			}
		}
		committed = filterAndSort(tasks, Filter{})
		return nil
	}, options...)
	if errors.Is(err, errAlreadyApplied) {
		return committed, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return s.List(Filter{}), false, nil
}

func (s *Store) Projects() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]struct{})
	for _, task := range s.tasks {
		if task.Project != "" {
			seen[task.Project] = struct{}{}
		}
	}
	projects := make([]string, 0, len(seen))
	for project := range seen {
		projects = append(projects, project)
	}
	sort.Slice(projects, func(i, j int) bool {
		return strings.ToLower(projects[i]) < strings.ToLower(projects[j])
	})
	return projects
}

func (s *Store) transact(change func(map[string]Task) error, options ...Mutation) error {
	return s.transactWithHistory(func(tasks map[string]Task, _ *[]HistoryRecord) error {
		return change(tasks)
	}, options...)
}

func (s *Store) transactWithHistory(change func(map[string]Task, *[]HistoryRecord) error, options ...Mutation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var option Mutation
	if len(options) > 0 {
		option = options[0]
	}
	if err := checkRevision(s.tasks, option.TaskID, option.ExpectedRevision); err != nil {
		return err
	}
	candidate := cloneMap(s.tasks)
	history := append([]HistoryRecord{}, s.history...)
	if err := change(candidate, &history); err != nil {
		return err
	}
	events, err := s.prepareChanges(candidate, option.Actor)
	if err != nil {
		return err
	}
	for _, record := range fullHistoryChanges(s.tasks, candidate, option.Actor, s.now) {
		history = append(history, record)
	}
	completeSince := s.completeHistorySince
	if completeSince.IsZero() {
		completeSince = s.now()
	}
	if err := s.persist(candidate, events, history, completeSince, s.partialLegacyHistory); err != nil {
		return err
	}
	s.tasks = candidate
	s.activity = events
	s.history = history
	s.completeHistorySince = completeSince
	s.needsMigrationBackup = false
	return nil
}

func (s *Store) persist(tasks map[string]Task, events []Activity, history []HistoryRecord, completeSince time.Time, partial bool) error {
	if s.needsMigrationBackup {
		stamp := s.now().UTC().Format("20060102T150405.000000000Z")
		backup := s.path + ".v1-" + stamp + ".backup"
		data, err := os.ReadFile(s.path)
		if err != nil {
			return fmt.Errorf("%w: read v1 migration backup: %v", ErrPersistence, err)
		}
		if err := os.WriteFile(backup, data, 0o600); err != nil {
			return fmt.Errorf("%w: write v1 migration backup: %v", ErrPersistence, err)
		}
	}
	state := persistedState{Version: stateVersion, Tasks: filterAndSort(tasks, Filter{}), Activity: events, History: history, CompleteHistorySince: completeSince, PartialLegacyHistory: partial}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: marshal task state: %v", ErrPersistence, err)
	}
	data = append(data, '\n')
	temporary := filepath.Join(filepath.Dir(s.path), ".tasks.json.tmp")
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return fmt.Errorf("%w: write temporary task state: %v", ErrPersistence, err)
	}
	if err := os.Rename(temporary, s.path); err != nil {
		return fmt.Errorf("%w: replace task state: %v", ErrPersistence, err)
	}
	if s.afterPersist != nil {
		s.afterPersist()
	}
	return nil
}

func (s *Store) createTask(tasks map[string]Task, input TaskInput) (Task, error) {
	normalized, err := normalizeInput(input, defaultTaskSource)
	if err != nil {
		return Task{}, err
	}
	id, err := s.newTaskID()
	if err != nil {
		return Task{}, err
	}
	order := nextOrder(tasks, normalized.Lane, "")
	if normalized.Order != nil {
		order = *normalized.Order
	}
	subtasks, err := s.makeSubtasks(normalized.Subtasks, normalized.Lane)
	if err != nil {
		return Task{}, err
	}
	now := s.now()
	task := Task{
		Revision: 1,
		ID:       id, Title: normalized.Title, Lane: normalized.Lane, Priority: normalized.Priority,
		Kind: normalized.Kind, Project: normalized.Project, Tag: normalized.Tag, Notes: normalized.Notes,
		Source: normalized.Source, Order: order, CreatedAt: now, UpdatedAt: now,
		Subtasks: subtasks,
	}
	if err := s.metadata(&task, input.Links, input.AgentContext, input.ReconcileMode); err != nil {
		return Task{}, err
	}
	tasks[id] = task
	return task, nil
}

func (s *Store) applyPatch(tasks map[string]Task, current Task, patch TaskPatch) (Task, error) {
	input := TaskInput{
		Title: current.Title, Lane: current.Lane, Kind: current.Kind, Priority: current.Priority, Project: current.Project,
		Tag: current.Tag, Notes: current.Notes, Source: current.Source,
	}
	if patch.Title != nil {
		input.Title = *patch.Title
	}
	if patch.Lane != nil {
		input.Lane = *patch.Lane
	}
	if patch.Kind != nil {
		input.Kind = *patch.Kind
	}
	if patch.Priority != nil {
		input.Priority = *patch.Priority
	}
	if patch.Project != nil {
		input.Project = *patch.Project
	}
	if patch.Tag != nil {
		input.Tag = *patch.Tag
	}
	if patch.Notes != nil {
		input.Notes = *patch.Notes
	}
	if patch.Source != nil {
		input.Source = *patch.Source
	}
	normalized, err := normalizeInput(input, current.Source)
	if err != nil {
		return Task{}, err
	}
	updated := cloneTask(current)
	updated.Revision = current.Revision + 1
	updated.Title = normalized.Title
	updated.Lane = normalized.Lane
	updated.Kind = normalized.Kind
	updated.Priority = normalized.Priority
	updated.Project = normalized.Project
	updated.Tag = normalized.Tag
	updated.Notes = normalized.Notes
	updated.Source = normalized.Source
	if patch.Subtasks != nil {
		updated.Subtasks, err = s.makeSubtasks(*patch.Subtasks, updated.Lane)
		if err != nil {
			return Task{}, err
		}
	}
	if patch.Order != nil {
		updated.Order = *patch.Order
	} else if updated.Lane != current.Lane {
		updated.Order = nextOrder(tasks, updated.Lane, current.ID)
	}
	if err := s.metadata(&updated, patch.Links, patch.AgentContext, patch.ReconcileMode); err != nil {
		return Task{}, err
	}
	updated.UpdatedAt = s.now()
	return updated, nil
}

func (s *Store) applyOperation(tasks map[string]Task, operation Operation, source string) error {
	op := operation.Op
	if op == "" {
		op = "create"
	}
	switch op {
	case "create":
		input := TaskInput{Subtasks: operation.Subtasks, Links: operation.Links, AgentContext: operation.AgentContext, ReconcileMode: operation.ReconcileMode}
		if operation.Title != nil {
			input.Title = *operation.Title
		}
		if operation.Lane != nil {
			input.Lane = *operation.Lane
		}
		if operation.Kind != nil {
			input.Kind = *operation.Kind
		}
		if operation.Priority != nil {
			input.Priority = *operation.Priority
		}
		if operation.Project != nil {
			input.Project = *operation.Project
		}
		if operation.Tag != nil {
			input.Tag = *operation.Tag
		}
		if operation.Notes != nil {
			input.Notes = *operation.Notes
		}
		input.Source = source
		if operation.Source != nil {
			input.Source = *operation.Source
		}
		input.Order = operation.Order
		_, err := s.createTask(tasks, input)
		return err
	case "patch":
		current, ok := tasks[operation.ID]
		if !ok {
			return fmt.Errorf("task %q: %w", operation.ID, ErrNotFound)
		}
		patch := TaskPatch{
			Links: operation.Links, AgentContext: operation.AgentContext, ReconcileMode: operation.ReconcileMode,
			Title: operation.Title, Lane: operation.Lane, Kind: operation.Kind, Priority: operation.Priority,
			Project: operation.Project, Tag: operation.Tag, Notes: operation.Notes,
			Source: operation.Source, Order: operation.Order,
		}
		if operation.Subtasks != nil {
			patch.Subtasks = &operation.Subtasks
		}
		updated, err := s.applyPatch(tasks, current, patch)
		if err == nil {
			tasks[operation.ID] = updated
		}
		return err
	case "subtask":
		task, ok := tasks[operation.ID]
		if !ok {
			return fmt.Errorf("task %q: %w", operation.ID, ErrNotFound)
		}
		if operation.Title == nil {
			return errors.New("subtask operation requires title")
		}
		if len(task.Subtasks) >= MaxSubtasks {
			return fmt.Errorf("task cannot contain more than %d subtasks", MaxSubtasks)
		}
		input := SubtaskInput{Title: *operation.Title}
		if operation.Lane != nil {
			input.Lane = *operation.Lane
		}
		if operation.Done != nil {
			input.Done = *operation.Done
		}
		normalized, err := normalizeSubtaskInput(input, task.Lane)
		if err != nil {
			return err
		}
		id, err := s.newSubtaskID()
		if err != nil {
			return err
		}
		task.Subtasks = append(task.Subtasks, Subtask{
			ID: id, Title: normalized.Title, Lane: normalized.Lane, Done: normalized.Done,
		})
		task.UpdatedAt = s.now()
		tasks[task.ID] = task
		return nil
	case "check":
		task, ok := tasks[operation.ID]
		if !ok {
			return fmt.Errorf("task %q: %w", operation.ID, ErrNotFound)
		}
		found := false
		for i := range task.Subtasks {
			if task.Subtasks[i].ID == operation.SubtaskID {
				done := true
				if operation.Done != nil {
					done = *operation.Done
				}
				if err := applySubtaskPatch(&task, i, SubtaskPatch{Done: &done}); err != nil {
					return err
				}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("subtask %q: %w", operation.SubtaskID, ErrNotFound)
		}
		task.UpdatedAt = s.now()
		tasks[task.ID] = task
		return nil
	case "link", "unlink":
		task, ok := tasks[operation.ID]
		if !ok {
			return ErrNotFound
		}
		if op == "link" {
			if operation.Link == nil {
				return errors.New("link operation requires link")
			}
			if _, err := s.upsertLink(&task, *operation.Link); err != nil {
				return err
			}
		} else {
			if err := removeLink(&task, operation.LinkID); err != nil {
				return err
			}
			task.UpdatedAt = s.now()
		}
		tasks[operation.ID] = task
		return nil
	case "delete":
		if _, ok := tasks[operation.ID]; !ok {
			return fmt.Errorf("task %q: %w", operation.ID, ErrNotFound)
		}
		delete(tasks, operation.ID)
		return nil
	default:
		return fmt.Errorf("unsupported op %q", op)
	}
}

func (s *Store) makeSubtasks(inputs SubtaskInputs, parentLane string) ([]Subtask, error) {
	result := make([]Subtask, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for i, input := range inputs {
		normalized, err := normalizeSubtaskInput(input, parentLane)
		if err != nil {
			return nil, fmt.Errorf("subtasks[%d]: %w", i, err)
		}
		id := input.ID
		if id == "" {
			var err error
			id, err = s.newSubtaskID()
			if err != nil {
				return nil, err
			}
		}
		if !strings.HasPrefix(id, "s") {
			return nil, fmt.Errorf("subtask id %q is invalid", id)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("duplicate subtask id %q", id)
		}
		seen[id] = struct{}{}
		result = append(result, Subtask{
			ID: id, Title: normalized.Title, Lane: normalized.Lane, Done: normalized.Done,
		})
	}
	return result, nil
}

func (s *Store) newTaskID() (string, error) {
	random, err := s.randomID(5)
	if err != nil {
		return "", fmt.Errorf("generate task id: %w", err)
	}
	millis := strconv.FormatInt(s.now().UnixMilli(), 36)
	return "t" + millis + random, nil
}

func (s *Store) newSubtaskID() (string, error) {
	random, err := s.randomID(7)
	if err != nil {
		return "", fmt.Errorf("generate subtask id: %w", err)
	}
	return "s" + random, nil
}

func randomBase36(length int) (string, error) {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	buffer := make([]byte, length)
	random := make([]byte, length)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	for i, value := range random {
		buffer[i] = alphabet[int(value)%len(alphabet)]
	}
	return string(buffer), nil
}

func nextOrder(tasks map[string]Task, lane, excludingID string) float64 {
	maxOrder := float64(0)
	for id, task := range tasks {
		if id != excludingID && task.Lane == lane && task.Order > maxOrder {
			maxOrder = task.Order
		}
	}
	return maxOrder + 1000
}

func filterAndSort(tasks map[string]Task, filter Filter) []Task {
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	result := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		if filter.Lane != "" && task.Lane != filter.Lane {
			continue
		}
		if filter.Project != "" && !strings.EqualFold(task.Project, filter.Project) {
			continue
		}
		if filter.Tag != "" && !strings.EqualFold(task.Tag, filter.Tag) {
			continue
		}
		if filter.Kind != "" && !strings.EqualFold(task.Kind, filter.Kind) {
			continue
		}
		if filter.Priority != 0 && task.Priority != filter.Priority {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(task.Title), query) &&
			!strings.Contains(strings.ToLower(task.Notes), query) &&
			!strings.Contains(strings.ToLower(task.Project), query) &&
			!subtasksContain(task.Subtasks, query) {
			continue
		}
		result = append(result, cloneTask(task))
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if LaneIndex(left.Lane) != LaneIndex(right.Lane) {
			return LaneIndex(left.Lane) < LaneIndex(right.Lane)
		}
		if left.Order != right.Order {
			return left.Order < right.Order
		}
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		return left.ID < right.ID
	})
	return result
}

func subtasksContain(subtasks []Subtask, query string) bool {
	for _, subtask := range subtasks {
		if strings.Contains(strings.ToLower(subtask.Title), query) {
			return true
		}
	}
	return false
}

func cloneMap(tasks map[string]Task) map[string]Task {
	result := make(map[string]Task, len(tasks))
	for id, task := range tasks {
		result[id] = cloneTask(task)
	}
	return result
}

func cloneTasks(tasks []Task) []Task {
	result := make([]Task, len(tasks))
	for i, task := range tasks {
		result[i] = cloneTask(task)
	}
	return result
}

func cloneTask(task Task) Task {
	links := make([]Link, len(task.Links))
	copy(links, task.Links)
	for i := range links {
		if links[i].StateChangedAt != nil {
			stamp := *links[i].StateChangedAt
			links[i].StateChangedAt = &stamp
		}
	}
	task.Links = links
	if len(task.Subtasks) == 0 {
		task.Subtasks = []Subtask{}
	} else {
		subtasks := make([]Subtask, len(task.Subtasks))
		copy(subtasks, task.Subtasks)
		task.Subtasks = subtasks
	}
	return task
}
