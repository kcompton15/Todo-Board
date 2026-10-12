package store

import (
	"strings"
	"unicode"
)

// foldKey lowercases and drops spaces, '_' and '-', so "Web App" and "WebApp" match.
func foldKey(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if r == '_' || r == '-' || unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// canonicalSpelling returns the most-used spelling on other cards with the same fold key as value.
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

func foldNames(tasks map[string]Task, selfID string, project, tag *string) {
	if project != nil {
		*project = canonicalSpelling(tasks, selfID, *project, func(task Task) string { return task.Project })
	}
	if tag != nil {
		*tag = canonicalSpelling(tasks, selfID, *tag, func(task Task) string { return task.Tag })
	}
}
