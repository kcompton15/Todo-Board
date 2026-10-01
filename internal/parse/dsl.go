package parse

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kcompton15/Todo-Board/internal/store"
)

var (
	markerValue = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	tagValue    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)
)

// Tasks parses the line-oriented import format. A leading tab or at least two
// spaces makes a line a subtask of the most recent task.
func Tasks(input string) ([]store.TaskInput, error) {
	scanner := bufio.NewScanner(strings.NewReader(input))
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	var tasks []store.TaskInput
	current := -1
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		raw := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		indented := strings.HasPrefix(raw, "\t") || strings.HasPrefix(raw, "  ")
		line := stripBullet(strings.TrimSpace(raw))
		if line == "" {
			continue
		}
		if indented {
			if current >= 0 {
				tasks[current].Subtasks = append(tasks[current].Subtasks, store.SubtaskInput{Title: line})
			}
			continue
		}
		task, err := parseTaskLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		tasks = append(tasks, task)
		current = len(tasks) - 1
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read task input: %w", err)
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("input contains no tasks")
	}
	return tasks, nil
}

func stripBullet(line string) string {
	if len(line) >= 2 && (line[0] == '-' || line[0] == '*' || line[0] == '+') && (line[1] == ' ' || line[1] == '\t') {
		return strings.TrimSpace(line[2:])
	}
	return line
}

func parseTaskLine(line string) (store.TaskInput, error) {
	fields := strings.Fields(line)
	task := store.TaskInput{Lane: "backlog", Priority: 2}
	titleFields := make([]string, 0, len(fields))
	seenTag := false
	seenProject := false
	seenPriority := false
	seenLane := false

	for _, field := range fields {
		switch {
		case !seenPriority && len(field) == 2 && field[0] == '!' && field[1] >= '1' && field[1] <= '3':
			task.Priority, _ = strconv.Atoi(field[1:])
			seenPriority = true
		case !seenLane && strings.HasPrefix(field, "@") && store.IsLane(strings.ToLower(strings.TrimPrefix(field, "@"))):
			task.Lane = strings.ToLower(strings.TrimPrefix(field, "@"))
			seenLane = true
		case !seenTag && strings.HasPrefix(field, "#") && validTag(strings.TrimPrefix(field, "#")):
			task.Tag = strings.TrimPrefix(field, "#")
			seenTag = true
		case !seenProject && strings.HasPrefix(field, "%") && validMarker(strings.TrimPrefix(field, "%"), store.MaxProjectLength):
			task.Project = strings.TrimPrefix(field, "%")
			seenProject = true
		default:
			titleFields = append(titleFields, field)
		}
	}
	task.Title = strings.Join(titleFields, " ")
	if task.Title == "" {
		return store.TaskInput{}, fmt.Errorf("task title is empty after parsing markers")
	}
	return task, nil
}

func validMarker(value string, maxLength int) bool {
	return value != "" && len(value) <= maxLength && markerValue.MatchString(value)
}

func validTag(value string) bool {
	return value != "" && len(value) <= store.MaxTagLength && tagValue.MatchString(value)
}
