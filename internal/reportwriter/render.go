package reportwriter

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/kcompton15/Todo-Board/internal/store"
)

var ErrNoPoints = errors.New("the model returned no talking points that cite cards in the evidence")

type Rendered struct {
	Markdown  string
	CardCount int
	Points    int
	Dropped   int
}

// Render keeps only points and blockers whose citations all exist in the evidence, merges repeats, and formats Markdown.
func Render(evidence store.Evidence, report Report) (Rendered, error) {
	known := citableCards(evidence)
	refs := observedRefs(evidence)
	cited := map[int]bool{}
	dropped := 0

	type renderedPoint struct {
		text    string
		cards   []int
		details []string
	}
	points := []*renderedPoint{}
	byText := map[string]*renderedPoint{}
	for _, point := range report.Points {
		text, cards, ok := cleanCited(point.Text, maxPointText, point.Cards, known)
		if !ok {
			dropped++
			continue
		}
		key := strings.ToLower(text)
		if existing := byText[key]; existing != nil {
			existing.cards = mergeCards(existing.cards, cards)
			existing.details = append(existing.details, point.Details...)
			continue
		}
		entry := &renderedPoint{text: text, cards: cards, details: append([]string{}, point.Details...)}
		byText[key] = entry
		points = append(points, entry)
	}

	if len(points) == 0 && len(evidence.Cards) > 0 {
		return Rendered{}, ErrNoPoints
	}

	lines := []string{}
	seenDetails := map[string]bool{}
	statedRefs := map[string]bool{}
	for _, point := range points {
		lines = append(lines, "- "+point.text+citation(point.cards))
		for _, card := range point.cards {
			cited[card] = true
		}
		for _, raw := range point.details {
			detail := oneLine(raw)
			key := strings.ToLower(detail)
			if detail == "" || utf8.RuneCountInString(detail) > maxDetailText || seenDetails[key] {
				continue
			}
			mentioned := mentionedRefs(detail, refs)
			if len(mentioned) > 0 && allStated(mentioned, statedRefs) {
				continue
			}
			seenDetails[key] = true
			for _, ref := range mentioned {
				statedRefs[ref] = true
			}
			lines = append(lines, "  - "+detail)
		}
	}

	blockers := []string{}
	seenBlockers := map[string]bool{}
	for _, blocker := range report.Blockers {
		text, cards, ok := cleanCited(blocker.Text, maxBlockerText, blocker.Cards, known)
		if !ok {
			dropped++
			continue
		}
		key := strings.ToLower(text)
		if seenBlockers[key] {
			continue
		}
		seenBlockers[key] = true
		for _, card := range cards {
			cited[card] = true
		}
		blockers = append(blockers, "- "+text+citation(cards))
	}
	if len(blockers) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "## Blockers")
		lines = append(lines, blockers...)
	}
	if len(lines) == 0 {
		lines = append(lines, "- Nothing recorded for this period.")
	}
	return Rendered{Markdown: strings.Join(lines, "\n"), CardCount: len(cited), Points: len(points), Dropped: dropped}, nil
}

// citableCards is every card number the evidence names, including carry-forward blockers and weekly in-flight cards.
func citableCards(evidence store.Evidence) map[int]bool {
	known := map[int]bool{}
	for _, card := range evidence.Cards {
		known[card.Number] = true
	}
	for _, blocker := range evidence.OpenBlockers {
		known[blocker.Number] = true
	}
	for _, card := range evidence.ActiveCards {
		known[card.Number] = true
	}
	delete(known, 0)
	return known
}

// observedRefs lists provider refs from observed lines; the reconciler can attach one ref to several cards.
func observedRefs(evidence store.Evidence) []string {
	seen := map[string]bool{}
	refs := []string{}
	for _, card := range evidence.Cards {
		for _, line := range card.Observed {
			fields := strings.Fields(line)
			if len(fields) == 0 || seen[fields[0]] {
				continue
			}
			seen[fields[0]] = true
			refs = append(refs, fields[0])
		}
	}
	return refs
}

func cleanCited(raw string, limit int, cards []int, known map[int]bool) (string, []int, bool) {
	text := oneLine(raw)
	if text == "" || utf8.RuneCountInString(text) > limit || len(cards) == 0 {
		return "", nil, false
	}
	for _, card := range cards {
		if !known[card] {
			return "", nil, false
		}
	}
	return text, mergeCards(nil, cards), true
}

func mergeCards(a, b []int) []int {
	out := append(append([]int{}, a...), b...)
	slices.Sort(out)
	return slices.Compact(out)
}

func mentionedRefs(text string, refs []string) []string {
	lower := strings.ToLower(text)
	out := []string{}
	for _, ref := range refs {
		if containsToken(lower, strings.ToLower(ref)) {
			out = append(out, ref)
		}
	}
	return out
}

// containsToken matches ref only when it is not part of a longer ref, so ABC-87 never matches ABC-871.
func containsToken(text, ref string) bool {
	for offset := 0; ref != ""; {
		index := strings.Index(text[offset:], ref)
		if index < 0 {
			return false
		}
		start, end := offset+index, offset+index+len(ref)
		if (start == 0 || !isRefChar(text[start-1])) && (end == len(text) || !isRefChar(text[end])) {
			return true
		}
		offset = start + 1
	}
	return false
}

func isRefChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '!' || c == '-' || c == '_' || c == '/'
}

func allStated(refs []string, stated map[string]bool) bool {
	for _, ref := range refs {
		if !stated[ref] {
			return false
		}
	}
	return true
}

func citation(cards []int) string {
	labels := make([]string, 0, len(cards))
	for _, card := range cards {
		labels = append(labels, fmt.Sprintf("#%d", card))
	}
	return " (" + strings.Join(labels, ", ") + ")"
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
