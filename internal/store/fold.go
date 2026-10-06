package store

import (
	"strings"
	"unicode"
)

// foldKey lowercases and collapses runs of spaces, '_' and '-' into one '-'.
func foldKey(value string) string {
	var b strings.Builder
	pending := false
	for _, r := range strings.ToLower(value) {
		if r == '_' || r == '-' || unicode.IsSpace(r) {
			pending = true
			continue
		}
		if pending && b.Len() > 0 {
			b.WriteByte('-')
		}
		pending = false
		b.WriteRune(r)
	}
	return b.String()
}

// canonicalSpelling returns the most-used spelling among other cards that shares value's fold key.
func canonicalSpelling(tasks map[string]Task, selfID, value string, field func(Task) string) string {
	key := foldKey(value)
	if key == "" {
		return value
	}
	counts := map[string]int{}
	for id, task := range tasks {
		if id == selfID {
			continue
		}
		if spelling := field(task); spelling != "" && foldKey(spelling) == key {
			counts[spelling]++
		}
	}
	best, bestCount := value, 0
	for spelling, count := range counts {
		if count > bestCount || (count == bestCount && preferSpelling(spelling, best)) {
			best, bestCount = spelling, count
		}
	}
	return best
}

func preferSpelling(a, b string) bool {
	aLower, bLower := a == strings.ToLower(a), b == strings.ToLower(b)
	if aLower != bLower {
		return aLower
	}
	return a < b
}

func taskProject(task Task) string { return task.Project }
func taskTag(task Task) string     { return task.Tag }

func foldNames(tasks map[string]Task, selfID string, project, tag *string) {
	if project != nil {
		*project = canonicalSpelling(tasks, selfID, *project, taskProject)
	}
	if tag != nil {
		*tag = canonicalSpelling(tasks, selfID, *tag, taskTag)
	}
}
