package reportwriter

import (
	"errors"
	"fmt"

	"github.com/kcompton15/Todo-Board/internal/claudecli"
)

const (
	maxPoints      = 8
	maxDetails     = 3
	maxBlockers    = 4
	maxCitations   = 8
	maxPointText   = 120
	maxDetailText  = 160
	maxBlockerText = 160
)

var schema = fmt.Sprintf(`{"type":"object","additionalProperties":false,"required":["points","blockers"],"properties":{`+
	`"points":{"type":"array","maxItems":%d,"items":{"type":"object","additionalProperties":false,"required":["text","cards","details"],"properties":{`+
	`"text":{"type":"string","minLength":1,"maxLength":%d},`+
	`"cards":{"type":"array","minItems":1,"maxItems":%d,"items":{"type":"integer","minimum":1}},`+
	`"details":{"type":"array","maxItems":%d,"items":{"type":"string","minLength":1,"maxLength":%d}}}}},`+
	`"blockers":{"type":"array","maxItems":%d,"items":{"type":"object","additionalProperties":false,"required":["text","cards"],"properties":{`+
	`"text":{"type":"string","minLength":1,"maxLength":%d},`+
	`"cards":{"type":"array","minItems":1,"maxItems":%d,"items":{"type":"integer","minimum":1}}}}}}}`,
	maxPoints, maxPointText, maxCitations, maxDetails, maxDetailText, maxBlockers, maxBlockerText, maxCitations)

type Report struct {
	Points   []Point   `json:"points"`
	Blockers []Blocker `json:"blockers"`
}

type Point struct {
	Text    string   `json:"text"`
	Cards   []int    `json:"cards"`
	Details []string `json:"details"`
}

type Blocker struct {
	Text  string `json:"text"`
	Cards []int  `json:"cards"`
}

// Parse strictly decodes the model's structured output and enforces the schema's array caps.
func Parse(output []byte) (Report, error) {
	var report Report
	if err := claudecli.DecodeStrict(output, &report); err != nil {
		return Report{}, err
	}
	if report.Points == nil || report.Blockers == nil {
		return Report{}, errors.New("model output is missing points or blockers")
	}
	if len(report.Points) > maxPoints || len(report.Blockers) > maxBlockers {
		return Report{}, errors.New("model output has too many points or blockers")
	}
	for _, point := range report.Points {
		if point.Cards == nil || point.Details == nil || len(point.Details) > maxDetails || len(point.Cards) > maxCitations {
			return Report{}, errors.New("model output has a malformed point")
		}
	}
	for _, blocker := range report.Blockers {
		if blocker.Cards == nil || len(blocker.Cards) > maxCitations {
			return Report{}, errors.New("model output has a malformed blocker")
		}
	}
	return report, nil
}
