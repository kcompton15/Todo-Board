package reportwriter

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kcompton15/Todo-Board/internal/store"
)

func testEvidence() store.Evidence {
	return store.Evidence{
		Kind: "weekly", Start: "2026-10-05", End: "2026-10-11",
		Cards: []store.EvidenceCard{
			{Number: 110, Title: "Ship export queue", Observed: []string{"ABC-871 Ready for Prod"}},
			{Number: 133, Title: "Export QA", Observed: []string{"ABC-871 Ready for Prod", "web!140 merged"}},
			{Number: 216, Title: "Login fix", Observed: []string{"ABC-871 Ready for Prod"}},
		},
		OpenBlockers: []store.EvidenceBlocker{{Number: 30, Title: "Watcher", Text: "waiting on state feed"}},
		ActiveCards:  []store.EvidenceCardRef{{Number: 74, Title: "Checkout flows"}},
	}
}

func TestRenderFormatsCitationsAndDetails(t *testing.T) {
	report := Report{
		Points: []Point{
			{Text: "Export queue ready for prod", Cards: []int{133, 110}, Details: []string{"ABC-871 Ready for Prod", "web!140 merged"}},
			{Text: "Checkout flow suite  still going", Cards: []int{74}, Details: []string{}},
		},
		Blockers: []Blocker{{Text: "State feed outage", Cards: []int{30}}},
	}
	got, err := Render(testEvidence(), report)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"- Export queue ready for prod (#110, #133)",
		"  - ABC-871 Ready for Prod",
		"  - web!140 merged",
		"- Checkout flow suite still going (#74)",
		"",
		"## Blockers",
		"- State feed outage (#30)",
	}, "\n")
	if got.Markdown != want {
		t.Fatalf("markdown:\n%s", got.Markdown)
	}
	if got.CardCount != 4 || got.Points != 2 || got.Dropped != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestRenderDropsUnknownCitations(t *testing.T) {
	report := Report{
		Points: []Point{
			{Text: "Real work", Cards: []int{216}, Details: []string{}},
			{Text: "Invented work", Cards: []int{216, 999}, Details: []string{"made up"}},
		},
		Blockers: []Blocker{{Text: "Invented blocker", Cards: []int{4}}},
	}
	got, err := Render(testEvidence(), report)
	if err != nil {
		t.Fatal(err)
	}
	if got.Markdown != "- Real work (#216)" || got.Dropped != 2 || got.CardCount != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestRenderStatesSharedRefOnce(t *testing.T) {
	report := Report{Points: []Point{
		{Text: "Export queue ready", Cards: []int{110}, Details: []string{"ABC-871 Ready for Prod"}},
		{Text: "export queue READY", Cards: []int{133}, Details: []string{"ABC-871 ready for prod"}},
		{Text: "Login fix ready", Cards: []int{216}, Details: []string{"ABC-871 in prod queue", "ABC-87 untouched"}},
	}, Blockers: []Blocker{}}
	got, err := Render(testEvidence(), report)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"- Export queue ready (#110, #133)",
		"  - ABC-871 Ready for Prod",
		"- Login fix ready (#216)",
		"  - ABC-87 untouched",
	}, "\n")
	if got.Markdown != want || got.Points != 2 {
		t.Fatalf("markdown:\n%s", got.Markdown)
	}
}

func TestRenderFailsWhenNoValidPointsRemain(t *testing.T) {
	report := Report{Points: []Point{
		{Text: "Unknown", Cards: []int{5}, Details: []string{}},
		{Text: strings.Repeat("x", maxPointText+1), Cards: []int{110}, Details: []string{}},
		{Text: "  ", Cards: []int{110}, Details: []string{}},
	}, Blockers: []Blocker{{Text: "Real blocker", Cards: []int{30}}}}
	if _, err := Render(testEvidence(), report); !errors.Is(err, ErrNoPoints) {
		t.Fatal(err)
	}
}

func TestRenderQuietPeriod(t *testing.T) {
	evidence := store.Evidence{Kind: "standup", Start: "2026-10-09", End: "2026-10-12"}
	got, err := Render(evidence, Report{Points: []Point{}, Blockers: []Blocker{}})
	if err != nil || got.Markdown != "- Nothing recorded for this period." || got.CardCount != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestParseRejectsMalformedOutput(t *testing.T) {
	valid := `{"points":[{"text":"a","cards":[1],"details":[]}],"blockers":[]}`
	if _, err := Parse([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	tooMany := Report{Points: make([]Point, maxPoints+1), Blockers: []Blocker{}}
	for i := range tooMany.Points {
		tooMany.Points[i] = Point{Text: "a", Cards: []int{1}, Details: []string{}}
	}
	overflow, err := json.Marshal(tooMany)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"extra field":   `{"points":[],"blockers":[],"summary":"x"}`,
		"missing":       `{"points":[]}`,
		"null details":  `{"points":[{"text":"a","cards":[1],"details":null}],"blockers":[]}`,
		"string card":   `{"points":[{"text":"a","cards":["#1"],"details":[]}],"blockers":[]}`,
		"many details":  `{"points":[{"text":"a","cards":[1],"details":["1","2","3","4"]}],"blockers":[]}`,
		"trailing":      valid + `{}`,
		"too many":      string(overflow),
		"not an object": `"- a (#1)"`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestSchemaIsValidJSON(t *testing.T) {
	var value map[string]any
	if err := json.Unmarshal([]byte(schema), &value); err != nil {
		t.Fatal(err)
	}
}
