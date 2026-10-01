package mcpserver

import (
	"context"
	"github.com/kcompton15/Todo-Board/internal/store"
	"net/http"
)

const linkInstructions = "Record every implementation MR including sibling repositories with scoped required links; references are examples/dependencies/history. After finalizing review findings, use log_review_delivered for covered scopes, check only completed steps, and close the review before returning the report without further confirmation. Receipts are supplemental: future recovery can also recognize the configured reviewer's matching substantive published GitLab comments, their approval, or merged/closed targets with coverage for every MR and review round. Historical reviews need no mandatory receipt. Inactivity alone is insufficient. This does not authorize GitLab writes. Set reconcileMode manual to exclude all future automatic updates; use open steps or manual mode for deployment/QA beyond merge. AgentContext is an API/history-visible hint, not secret."

type LinkTaskInput struct {
	ID               string          `json:"id"`
	Link             store.LinkInput `json:"link"`
	ExpectedRevision *uint64         `json:"expectedRevision,omitempty"`
}
type UnlinkTaskInput struct {
	ID               string  `json:"id"`
	LinkID           string  `json:"linkId"`
	ExpectedRevision *uint64 `json:"expectedRevision,omitempty"`
}
type ReviewDeliveredInput struct {
	ID               string  `json:"id"`
	Text             string  `json:"text"`
	SubtaskID        string  `json:"subtaskId,omitempty"`
	ExpectedRevision *uint64 `json:"expectedRevision,omitempty"`
}

func (c *BoardClient) LinkTask(ctx context.Context, input LinkTaskInput) (store.Link, error) {
	var result store.Link
	if err := validateID("task id", input.ID); err != nil {
		return result, err
	}
	err := c.do(ctx, http.MethodPost, "/api/tasks/"+input.ID+"/links", nil, input.Link, &result, input.ExpectedRevision)
	return result, err
}
func (c *BoardClient) UnlinkTask(ctx context.Context, input UnlinkTaskInput) (map[string]any, error) {
	if err := validateID("task id", input.ID); err != nil {
		return nil, err
	}
	if err := validateID("link id", input.LinkID); err != nil {
		return nil, err
	}
	var result map[string]any
	err := c.do(ctx, http.MethodDelete, "/api/tasks/"+input.ID+"/links/"+input.LinkID, nil, nil, &result, input.ExpectedRevision)
	return result, err
}
func (c *BoardClient) LogReviewDelivered(ctx context.Context, input ReviewDeliveredInput) (store.WorkLogEntry, error) {
	var result store.WorkLogEntry
	if err := validateID("task id", input.ID); err != nil {
		return result, err
	}
	body := store.WorkLogInput{Kind: store.WorkLogReviewDelivered, Text: input.Text, SubtaskID: input.SubtaskID}
	err := c.do(ctx, http.MethodPost, "/api/tasks/"+input.ID+"/work-log", nil, body, &result, input.ExpectedRevision)
	return result, err
}

func linkSchema() map[string]any {
	return objectSchema(map[string]any{"id": stringProperty("Existing server link ID; omit for additions"), "ref": stringProperty("Jira key or namespace/project!iid"), "url": stringProperty("Canonical Jira or GitLab browse URL"), "subtaskId": stringProperty("Existing child ID; omit for card scope"), "role": enumProperty("Completion intent; default required", []string{"required", "reference"}), "state": stringProperty("Observed provider state"), "stateCategory": enumProperty("Jira category", []string{"", "new", "indeterminate", "done"})}, nil)
}
func linksSchema() map[string]any {
	return map[string]any{"type": "array", "items": linkSchema(), "maxItems": store.MaxLinks}
}
func linkTools() []toolDefinition {
	revision := map[string]any{"type": "integer", "minimum": 1}
	return []toolDefinition{
		{Name: "link_task", Title: "Link Jira or MR", Description: "Upsert an exact scoped target; required by default, references never authorize completion.", InputSchema: objectSchema(map[string]any{"id": stringProperty("Full card ID"), "link": linkSchema(), "expectedRevision": revision}, []string{"id", "link"}), Annotations: annotations(false, false, true)},
		{Name: "unlink_task", Title: "Remove a link", Description: "Remove a link owned by this card.", InputSchema: objectSchema(map[string]any{"id": stringProperty("Full card ID"), "linkId": stringProperty("Full link ID"), "expectedRevision": revision}, []string{"id", "linkId"}), Annotations: annotations(false, true, false)},
		{Name: "log_review_delivered", Title: "Record finalized review delivery", Description: "After finalizing findings, record the covered review scope, complete only finished steps, then close your review before returning the report. Supplemental receipt; never posts to GitLab.", InputSchema: objectSchema(map[string]any{"id": stringProperty("Full review card ID"), "text": stringProperty("Finalized report or findings"), "subtaskId": stringProperty("Optional covered child ID"), "expectedRevision": revision}, []string{"id", "text"}), Annotations: annotations(false, false, false)},
	}
}
