package reconcile

import (
	"context"
	"errors"
	"github.com/kcompton15/Todo-Board/internal/store"
)

// projectDeterministic never persists or creates receipt evidence.
func projectDeterministic(tasks []store.Task, plan PlanResult) ([]store.Task, error) {
	result := append([]store.Task{}, tasks...)
	for i := range result {
		result[i].Links = append([]store.Link{}, result[i].Links...)
		result[i].Subtasks = append([]store.Subtask{}, result[i].Subtasks...)
	}
	for _, envelope := range plan.Envelopes {
		for i := range result {
			task := &result[i]
			if task.ID != envelope.TaskID {
				continue
			}
			changed := false
			for _, op := range envelope.Batch.Ops {
				switch op.Op {
				case "work_log":
					continue
				case "patch":
					if op.Lane == nil {
						return nil, errors.New("unsupported hypothetical patch")
					}
					task.Lane = *op.Lane
				case "check":
					for j := range task.Subtasks {
						if task.Subtasks[j].ID == op.SubtaskID {
							task.Subtasks[j].Done = true
							task.Subtasks[j].Lane = "done"
						}
					}
				case "link":
					if op.Link == nil {
						return nil, errors.New("missing hypothetical link")
					}
					kind, ref, destination, err := store.CanonicalLink(op.Link.Ref)
					if err != nil {
						return nil, err
					}
					index := -1
					for j, l := range task.Links {
						if l.Ref == ref && l.SubtaskID == op.Link.SubtaskID {
							index = j
						}
					}
					if index < 0 {
						task.Links = append(task.Links, store.Link{Kind: kind, Ref: ref, URL: destination, SubtaskID: op.Link.SubtaskID, Role: op.Link.Role})
						index = len(task.Links) - 1
					}
					if op.Link.State != nil {
						task.Links[index].State = *op.Link.State
					}
					if op.Link.StateCategory != nil {
						task.Links[index].StateCategory = *op.Link.StateCategory
					}
				default:
					return nil, errors.New("unsupported hypothetical operation")
				}
				changed = true
			}
			if changed {
				task.Revision++
			}
		}
	}
	return result, nil
}

func (r *Runner) modelPhase(ctx context.Context, options RunOptions, tasks []store.Task, evidence Evidence, history HistoryEvidence, result *RunResult) {
	result.Model = ModelResult{Status: "disabled", Hypothetical: options.DryRun, Envelopes: []Envelope{}, Rejections: []string{}}
	if options.NoLLM || r.Model == nil {
		return
	}
	fail := func(err error) {
		result.Model.Status = "skipped"
		result.Model.Rejections = append(result.Model.Rejections, err.Error())
		result.Errors = append(result.Errors, "optional model phase incomplete; deterministic commits retained")
	}
	var err error
	if options.DryRun {
		tasks, err = projectDeterministic(tasks, result.Plan)
	} else {
		tasks, err = r.Board.Tasks(ctx)
		if err == nil {
			selected := []store.Task{}
			history = HistoryEvidence{}
			for _, task := range tasks {
				if (options.TaskID != "" && task.ID != options.TaskID) || !eligible(task) {
					continue
				}
				h, e := r.Board.History(ctx, task.ID)
				if e != nil {
					err = e
					break
				}
				history[task.ID] = h
				selected = append(selected, task)
			}
			tasks = selected
		}
	}
	if err != nil {
		fail(err)
		return
	}
	any := false
	for _, task := range tasks {
		if eligible(task) {
			any = true
		}
	}
	if !any {
		result.Model.Status = "no eligible cards"
		return
	}
	input, truncated, err := modelBundle(tasks, evidence, history, options.DryRun)
	result.Model.Truncated = truncated
	if err != nil {
		fail(err)
		return
	}
	proposals, err := r.Model.Suggest(ctx, input)
	if err != nil {
		fail(err)
		return
	}
	result.Model = validateProposals(tasks, evidence, history, proposals)
	result.Model.Truncated = truncated
	result.Model.Hypothetical = options.DryRun
	for _, envelope := range result.Model.Envelopes {
		if options.DryRun {
			continue
		}
		replayed, err := r.Board.Apply(ctx, envelope)
		if err != nil {
			var api *APIError
			if errors.As(err, &api) && api.Status == 409 {
				result.Conflicted++
			} else {
				fail(err)
			}
			continue
		}
		if replayed {
			result.Replayed++
		} else {
			result.Changed++
			if completes(envelope) {
				title := ""
				for _, task := range tasks {
					if task.ID == envelope.TaskID {
						title = task.Title
					}
				}
				result.ChangedCards = append(result.ChangedCards, CardRef{ID: envelope.TaskID, Title: title})
			}
		}
	}
}
