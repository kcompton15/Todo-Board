package reportwriter

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/kcompton15/Todo-Board/internal/claudecli"
	"github.com/kcompton15/Todo-Board/internal/store"
)

const maxEvidenceInput = 128 << 10

type Model interface {
	Run(context.Context, claudecli.Request) (json.RawMessage, error)
}

type Writer struct {
	Model     Model
	ModelName string
	Budget    float64
	Timeout   time.Duration
}

// Write sends the evidence packet to the model as data and renders the validated talking points.
func (w Writer) Write(ctx context.Context, kind string, evidence []byte) (Rendered, error) {
	if kind != "standup" && kind != "weekly" {
		return Rendered{}, errors.New("unknown report kind")
	}
	if len(evidence) > maxEvidenceInput {
		return Rendered{}, errors.New("evidence packet exceeds the model input limit")
	}
	var packet store.Evidence
	if err := json.Unmarshal(evidence, &packet); err != nil {
		return Rendered{}, errors.New("evidence packet is not valid JSON")
	}
	if packet.Kind != kind {
		return Rendered{}, errors.New("evidence kind does not match the job")
	}
	output, err := w.Model.Run(ctx, claudecli.Request{SystemPrompt: systemPrompt(kind), Schema: schema, Input: evidence, Model: w.ModelName, Budget: w.Budget, Timeout: w.Timeout})
	if err != nil {
		return Rendered{}, err
	}
	report, err := Parse(output)
	if err != nil {
		return Rendered{}, err
	}
	return Render(packet, report)
}
