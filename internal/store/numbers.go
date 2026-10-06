package store

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// assignMissingNumbers numbers unnumbered cards in (createdAt, id) order and returns the next free number.
func assignMissingNumbers(tasks map[string]Task, persistedNext int) (int, error) {
	if persistedNext < 0 {
		return 0, fmt.Errorf("nextNumber %d is negative", persistedNext)
	}
	used := make(map[int]string, len(tasks))
	highest := 0
	var missing []Task
	for id, task := range tasks {
		if task.Number < 0 {
			return 0, fmt.Errorf("task %s has negative number %d", id, task.Number)
		}
		if task.Number == 0 {
			missing = append(missing, task)
			continue
		}
		if other, exists := used[task.Number]; exists {
			return 0, fmt.Errorf("tasks %s and %s share number %d", other, id, task.Number)
		}
		used[task.Number] = id
		highest = max(highest, task.Number)
	}
	next := max(highest+1, persistedNext)
	sort.Slice(missing, func(i, j int) bool {
		if !missing[i].CreatedAt.Equal(missing[j].CreatedAt) {
			return missing[i].CreatedAt.Before(missing[j].CreatedAt)
		}
		return missing[i].ID < missing[j].ID
	})
	for _, task := range missing {
		task.Number = next
		next++
		tasks[task.ID] = task
	}
	for id, task := range tasks {
		if err := numberSubtasks(&task); err != nil {
			return 0, fmt.Errorf("task %s: %w", id, err)
		}
		tasks[id] = task
	}
	return next, nil
}

func numberSubtasks(task *Task) error {
	if task.NextSubtaskNumber < 0 {
		return fmt.Errorf("nextSubtaskNumber %d is negative", task.NextSubtaskNumber)
	}
	seen := make(map[int]bool, len(task.Subtasks))
	highest := 0
	for _, sub := range task.Subtasks {
		if sub.Number < 0 {
			return fmt.Errorf("subtask %s has negative number %d", sub.ID, sub.Number)
		}
		if sub.Number == 0 {
			continue
		}
		if seen[sub.Number] {
			return fmt.Errorf("subtasks share number %d", sub.Number)
		}
		seen[sub.Number] = true
		highest = max(highest, sub.Number)
	}
	task.NextSubtaskNumber = max(task.NextSubtaskNumber, highest+1)
	for i := range task.Subtasks {
		if task.Subtasks[i].Number == 0 {
			task.Subtasks[i].Number = takeSubtaskNumber(task)
		}
	}
	return nil
}

func takeSubtaskNumber(task *Task) int {
	number := max(task.NextSubtaskNumber, 1)
	for _, sub := range task.Subtasks {
		number = max(number, sub.Number+1)
	}
	task.NextSubtaskNumber = number + 1
	return number
}

// parseNumberRef accepts N, #N, N.M and #N.M. The step number is zero when absent.
func parseNumberRef(ref string) (card, step int, ok bool) {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "#")
	cardPart, stepPart, hasStep := strings.Cut(ref, ".")
	card, ok = parsePositive(cardPart)
	if !ok {
		return 0, 0, false
	}
	if !hasStep {
		return card, 0, true
	}
	step, ok = parsePositive(stepPart)
	if !ok {
		return 0, 0, false
	}
	return card, step, true
}

func parsePositive(value string) (int, bool) {
	if value == "" || len(value) > 9 {
		return 0, false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 {
		return 0, false
	}
	return number, true
}

// Resolve finds a card by full ID or by N, #N, N.M or #N.M; a step reference also returns the step.
func (s *Store) Resolve(ref string) (Task, *Subtask, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if task, ok := s.tasks[ref]; ok {
		return cloneTask(task), nil, nil
	}
	card, step, ok := parseNumberRef(ref)
	if !ok {
		return Task{}, nil, ErrNotFound
	}
	for _, task := range s.tasks {
		if task.Number != card {
			continue
		}
		task = cloneTask(task)
		if step == 0 {
			return task, nil, nil
		}
		for i := range task.Subtasks {
			if task.Subtasks[i].Number == step {
				sub := task.Subtasks[i]
				return task, &sub, nil
			}
		}
		return Task{}, nil, ErrNotFound
	}
	return Task{}, nil, ErrNotFound
}

// FindSubtask matches a step by full ID, by M or #M, or by N.M when N is this card's number.
func FindSubtask(task Task, ref string) (Subtask, bool) {
	for _, sub := range task.Subtasks {
		if sub.ID == ref {
			return sub, true
		}
	}
	card, step, ok := parseNumberRef(ref)
	if !ok {
		return Subtask{}, false
	}
	if step == 0 {
		card, step = task.Number, card
	}
	if card != task.Number {
		return Subtask{}, false
	}
	for _, sub := range task.Subtasks {
		if sub.Number == step {
			return sub, true
		}
	}
	return Subtask{}, false
}
