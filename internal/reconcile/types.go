// Package reconcile plans evidence-backed board updates without provider writes.
package reconcile

import (
	"context"
	"github.com/kcompton15/Todo-Board/internal/store"
	"time"
)

const RuleVersion = "m3-v1"

type IssueState struct{ Key, Status, Category string }
type MRState struct {
	Project                            string
	IID                                int
	State, SourceBranch, Title, WebURL string
}
type ReviewEvent struct {
	ID        string     `json:"id"`
	URL       string     `json:"url"`
	AuthorID  int        `json:"authorId"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	Kind      string     `json:"kind"`
	Body      string     `json:"body,omitempty"`
	System    bool       `json:"system"`
	Bot       bool       `json:"bot"`
	Diff      bool       `json:"diff"`
}
type ReviewActivity struct {
	ReviewerID int
	Complete   bool
	Events     []ReviewEvent
}
type Observation struct {
	Issue       *IssueState
	MR          *MRState
	Review      *ReviewActivity
	Unavailable string
	FetchedAt   time.Time
}
type Evidence map[string]Observation
type CardHistory struct {
	Head     string
	Records  []store.HistoryRecord
	Complete bool
}
type HistoryEvidence map[string]CardHistory
type Envelope struct {
	TaskID  string            `json:"taskId"`
	Batch   store.OpsEnvelope `json:"batch"`
	Reasons []string          `json:"reasons"`
}
type Decision struct {
	TaskID string `json:"taskId"`
	Scope  string `json:"scope,omitempty"`
	Done   bool   `json:"done"`
	Reason string `json:"reason"`
}
type PlanResult struct {
	Envelopes []Envelope `json:"envelopes"`
	Decisions []Decision `json:"decisions"`
}

type Jira interface {
	Issues(context.Context, []string) (map[string]IssueState, error)
}
type GitLab interface {
	MR(context.Context, string, int) (MRState, error)
	SearchMRs(context.Context, string) ([]MRState, error)
	ReviewActivity(context.Context, string, int) (ReviewActivity, error)
}
