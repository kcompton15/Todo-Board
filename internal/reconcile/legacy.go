package reconcile

import (
	"github.com/kcompton15/Todo-Board/internal/store"
	"regexp"
	"strings"
)

var mrURL = regexp.MustCompile(`https://gitlab\.com/[^\s<>"'\)\]]+`)
var incidental = regexp.MustCompile(`(?i)\b(example|dependency|depends on|previous|prior|historical|history)\b`)

func issueMentionPattern(prefixes []string) *regexp.Regexp {
	if len(prefixes) == 0 {
		return nil
	}
	quoted := make([]string, len(prefixes))
	for i, prefix := range prefixes {
		quoted[i] = regexp.QuoteMeta(prefix)
	}
	return regexp.MustCompile(`\b(?:` + strings.Join(quoted, "|") + `)-[1-9][0-9]*\b`)
}

// Text discoveries never authorize completion, even if a mentioned issue is Done.
func textIssueLinks(task store.Task, prefixes []string) []store.Link {
	result := []store.Link{}
	mention := issueMentionPattern(prefixes)
	if mention == nil {
		return result
	}
	seen := map[string]bool{}
	add := func(text, scope string) {
		for _, indices := range mention.FindAllStringIndex(text, -1) {
			start, end := indices[0], indices[1]
			if (start > 0 && strings.ContainsRune("_-", rune(text[start-1]))) || (end < len(text) && strings.ContainsRune("_-", rune(text[end]))) {
				continue
			}
			ref := text[start:end]
			identity := ref + "/" + scope
			if seen[identity] {
				continue
			}
			seen[identity] = true
			kind, canonical, destination, err := store.CanonicalLink(ref)
			if err != nil {
				continue
			}
			result = append(result, store.Link{Kind: kind, Ref: canonical, URL: destination, Role: "reference", SubtaskID: scope})
		}
	}
	add(task.Title+"\n"+task.Notes, "")
	for _, sub := range task.Subtasks {
		add(sub.Title, sub.ID)
	}
	return result
}

func legacyLinks(task store.Task) ([]store.Link, []string) {
	links := []store.Link{}
	warnings := []string{}
	if task.Kind != "review" {
		return links, warnings
	}
	seen := map[string]bool{}
	for _, l := range task.Links {
		seen[l.Ref+"\x00"+l.SubtaskID] = true
	}
	add := func(text, scope string) {
		priorSection := false
		for _, line := range strings.Split(text, "\n") {
			urls := mrURL.FindAllString(line, -1)
			if len(urls) == 0 && strings.TrimSpace(line) != "" {
				priorSection = incidental.MatchString(line)
			}
			for _, raw := range urls {
				raw = strings.TrimRight(raw, ".,;")
				kind, ref, destination, err := store.CanonicalLink(raw)
				if err != nil || kind != "mr" {
					warnings = append(warnings, "ambiguous legacy MR URL")
					continue
				}
				key := ref + "\x00" + scope
				if seen[key] {
					continue
				}
				seen[key] = true
				role := "required"
				if priorSection || incidental.MatchString(line) || (scope != "" && !regexp.MustCompile(`(?i)\breview\b`).MatchString(line)) {
					role = "reference"
				}
				remainder := strings.TrimSpace(mrURL.ReplaceAllString(line, ""))
				if remainder != "" && !regexp.MustCompile(`(?i)(review|\bapi\b|\bweb\b|\bmr\b|^[\s,:;*-]*$)`).MatchString(remainder) {
					role = "reference"
					warnings = append(warnings, "unclear legacy target kept reference: "+ref)
				}
				links = append(links, store.Link{Kind: kind, Ref: ref, URL: destination, SubtaskID: scope, Role: role})
			}
		}
	}
	add(task.Title, "")
	add(task.Notes, "")
	for _, sub := range task.Subtasks {
		add(sub.Title, sub.ID)
	}
	if len(task.Links)+len(links) > store.MaxLinks {
		return nil, append(warnings, "legacy links exceed card limit")
	}
	return links, warnings
}
