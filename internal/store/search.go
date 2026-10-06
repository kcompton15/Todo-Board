package store

import (
	"regexp"
	"strconv"
	"strings"
)

var numberToken = regexp.MustCompile(`^#?([0-9]{1,9})(?:\.[0-9]{1,9})?$`)

func searchTokens(query string) []string {
	var tokens []string
	seen := map[string]bool{}
	for _, token := range strings.Fields(strings.ToLower(query)) {
		if !seen[token] {
			seen[token] = true
			tokens = append(tokens, token)
		}
	}
	return tokens
}

func wordStart(text, token string) bool {
	for offset := 0; ; {
		at := strings.Index(text[offset:], token)
		if at < 0 {
			return false
		}
		at += offset
		if at == 0 || !isWordByte(text[at-1]) || !isWordByte(token[0]) {
			return true
		}
		offset = at + 1
	}
}

func isWordByte(b byte) bool {
	return b >= 0x80 || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

// searchScore counts matched tokens: an exact card number is worth 3, a title word hit 2, anything else 1.
func searchScore(task Task, tokens []string) int {
	title := strings.ToLower(task.Title)
	fields := []string{strings.ToLower(task.Notes), strings.ToLower(task.Project), strings.ToLower(task.Tag)}
	for _, subtask := range task.Subtasks {
		fields = append(fields, strings.ToLower(subtask.Title))
	}
	for _, link := range task.Links {
		fields = append(fields, strings.ToLower(link.Ref), strings.ToLower(link.URL))
	}
	score := 0
	for _, token := range tokens {
		if match := numberToken.FindStringSubmatch(token); match != nil && task.Number > 0 {
			if number, err := strconv.Atoi(match[1]); err == nil && number == task.Number {
				score += 3
				continue
			}
		}
		if strings.Contains(title, token) {
			if wordStart(title, token) {
				score += 2
			} else {
				score++
			}
			continue
		}
		for _, field := range fields {
			if strings.Contains(field, token) {
				score++
				break
			}
		}
	}
	return score
}
