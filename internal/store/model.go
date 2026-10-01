package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxTitleLength    = 400
	MaxProjectLength  = 80
	MaxTagLength      = 40
	MaxNotesLength    = 8000
	MaxSourceLength   = 20
	MaxSubtasks       = 60
	MaxSubtaskTitle   = 300
	defaultTaskSource = "web"
	MaxWorkLogText    = 8000
)

var (
	ErrNotFound    = errors.New("not found")
	ErrPersistence = errors.New("persistence failure")
	LaneOrder      = []string{"backlog", "today", "doing", "blocked", "done"}
	KindOrder      = []string{"work", "followup", "review", "probe"}
)

type Task struct {
	Links         []Link    `json:"links"`
	AgentContext  string    `json:"agentContext,omitempty"`
	ReconcileMode string    `json:"reconcileMode"`
	Revision      uint64    `json:"revision"`
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	Lane          string    `json:"lane"`
	Kind          string    `json:"kind"`
	Priority      int       `json:"priority"`
	Project       string    `json:"project"`
	Tag           string    `json:"tag"`
	Notes         string    `json:"notes"`
	Source        string    `json:"source"`
	Order         float64   `json:"order"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Subtasks      []Subtask `json:"subtasks"`
}

type Subtask struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Lane  string `json:"lane"`
	Done  bool   `json:"done"`
}

type SubtaskInput struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Lane  string `json:"lane"`
	Done  bool   `json:"done"`
}

type SubtaskInputs []SubtaskInput

func (s *SubtaskInputs) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("subtasks must be an array: %w", err)
	}
	if len(raw) > MaxSubtasks {
		return fmt.Errorf("subtasks cannot contain more than %d items", MaxSubtasks)
	}

	out := make(SubtaskInputs, 0, len(raw))
	for i, item := range raw {
		var title string
		if err := json.Unmarshal(item, &title); err == nil {
			out = append(out, SubtaskInput{Title: title})
			continue
		}
		var input SubtaskInput
		if err := json.Unmarshal(item, &input); err != nil {
			return fmt.Errorf("subtasks[%d] must be a string or object", i)
		}
		out = append(out, input)
	}
	*s = out
	return nil
}

type TaskInput struct {
	Links         *[]LinkInput  `json:"links,omitempty"`
	AgentContext  *string       `json:"agentContext,omitempty"`
	ReconcileMode *string       `json:"reconcileMode,omitempty"`
	Title         string        `json:"title"`
	Lane          string        `json:"lane"`
	Kind          string        `json:"kind"`
	Priority      int           `json:"priority"`
	Project       string        `json:"project"`
	Tag           string        `json:"tag"`
	Notes         string        `json:"notes"`
	Source        string        `json:"source"`
	Order         *float64      `json:"order,omitempty"`
	Subtasks      SubtaskInputs `json:"subtasks"`
}

type TaskPatch struct {
	Links         *[]LinkInput   `json:"links,omitempty"`
	AgentContext  *string        `json:"agentContext,omitempty"`
	ReconcileMode *string        `json:"reconcileMode,omitempty"`
	Title         *string        `json:"title"`
	Lane          *string        `json:"lane"`
	Kind          *string        `json:"kind"`
	Priority      *int           `json:"priority"`
	Project       *string        `json:"project"`
	Tag           *string        `json:"tag"`
	Notes         *string        `json:"notes"`
	Source        *string        `json:"source"`
	Order         *float64       `json:"order"`
	Subtasks      *SubtaskInputs `json:"subtasks"`
}

type SubtaskPatch struct {
	Title *string `json:"title"`
	Lane  *string `json:"lane"`
	Done  *bool   `json:"done"`
}

type WorkLogKind string

const (
	WorkLogReviewDelivered WorkLogKind = "review_delivered"
	WorkLogProgress        WorkLogKind = "progress"
	WorkLogDecision        WorkLogKind = "decision"
	WorkLogBlocker         WorkLogKind = "blocker"
	WorkLogBlockerResolved WorkLogKind = "blocker_resolved"
	WorkLogNextStep        WorkLogKind = "next_step"
)

func IsWorkLogKind(kind WorkLogKind) bool {
	switch kind {
	case WorkLogProgress, WorkLogDecision, WorkLogBlocker, WorkLogBlockerResolved, WorkLogNextStep, WorkLogReviewDelivered:
		return true
	default:
		return false
	}
}

// OccurredAt is when the work happened; RecordedAt is when the board received it.
type WorkLogEntry struct {
	ActionID          string      `json:"actionId,omitempty"`
	ActionHash        string      `json:"actionHash,omitempty"`
	ID                string      `json:"id"`
	TaskID            string      `json:"taskId"`
	SubtaskID         string      `json:"subtaskId,omitempty"`
	Kind              WorkLogKind `json:"kind"`
	Text              string      `json:"text"`
	Actor             string      `json:"actor"`
	OccurredAt        time.Time   `json:"occurredAt"`
	RecordedAt        time.Time   `json:"recordedAt"`
	SessionID         string      `json:"sessionId,omitempty"`
	Origin            string      `json:"origin"`
	CorrectsID        string      `json:"correctsId,omitempty"`
	ResolvesBlockerID string      `json:"resolvesBlockerId,omitempty"`
	NeedsReview       bool        `json:"needsReview,omitempty"`
}

type WorkLogInput struct {
	ActionID          string `json:"actionId,omitempty"`
	actionHash        string
	TaskID            string      `json:"taskId"`
	SubtaskID         string      `json:"subtaskId,omitempty"`
	Kind              WorkLogKind `json:"kind"`
	Text              string      `json:"text"`
	OccurredAt        time.Time   `json:"occurredAt,omitempty"`
	SessionID         string      `json:"sessionId,omitempty"`
	Origin            string      `json:"origin,omitempty"`
	CorrectsID        string      `json:"correctsId,omitempty"`
	ResolvesBlockerID string      `json:"resolvesBlockerId,omitempty"`
	NeedsReview       bool        `json:"needsReview,omitempty"`
}

// Legacy v1 activity is kept separately and marked partial.
type HistoryRecord struct {
	ID        string        `json:"id"`
	TaskID    string        `json:"taskId"`
	SubtaskID string        `json:"subtaskId,omitempty"`
	At        time.Time     `json:"at"`
	Actor     string        `json:"actor"`
	Kind      string        `json:"kind"`
	Title     string        `json:"title,omitempty"`
	Changes   []FieldChange `json:"changes,omitempty"`
	WorkLog   *WorkLogEntry `json:"workLog,omitempty"`
	Partial   bool          `json:"partial,omitempty"`
}

type HistoryFilter struct {
	TaskID string
	Limit  int
	Before string
}

type HistoryPage struct {
	Records              []HistoryRecord `json:"records"`
	NextBefore           string          `json:"nextBefore,omitempty"`
	PartialLegacyHistory bool            `json:"partialLegacyHistory"`
}

type Operation struct {
	Links            *[]LinkInput  `json:"links,omitempty"`
	Link             *LinkInput    `json:"link,omitempty"`
	LinkID           string        `json:"linkId,omitempty"`
	AgentContext     *string       `json:"agentContext,omitempty"`
	ReconcileMode    *string       `json:"reconcileMode,omitempty"`
	ExpectedRevision *uint64       `json:"expectedRevision,omitempty"`
	Op               string        `json:"op"`
	ID               string        `json:"id"`
	WorkLog          *WorkLogInput `json:"workLog,omitempty"`
	SubtaskID        string        `json:"subtaskId"`
	Done             *bool         `json:"done,omitempty"`
	Title            *string       `json:"title,omitempty"`
	Lane             *string       `json:"lane,omitempty"`
	Kind             *string       `json:"kind,omitempty"`
	Priority         *int          `json:"priority,omitempty"`
	Project          *string       `json:"project,omitempty"`
	Tag              *string       `json:"tag,omitempty"`
	Notes            *string       `json:"notes,omitempty"`
	Source           *string       `json:"source,omitempty"`
	Order            *float64      `json:"order,omitempty"`
	Subtasks         SubtaskInputs `json:"subtasks"`
}

type OpsEnvelope struct {
	ExpectedHistoryHeads map[string]string `json:"expectedHistoryHeads,omitempty"`
	Source               string            `json:"source"`
	Ops                  []Operation       `json:"ops"`
}

type Filter struct {
	Lane     string
	Project  string
	Tag      string
	Kind     string
	Priority int
	Query    string
}

func ValidateNoNullFields(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	return rejectNull(value, "request")
}

func rejectNull(value any, path string) error {
	switch typed := value.(type) {
	case nil:
		return fmt.Errorf("%s cannot be null", path)
	case map[string]any:
		for key, child := range typed {
			if err := rejectNull(child, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range typed {
			if err := rejectNull(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func IsLane(lane string) bool {
	for _, candidate := range LaneOrder {
		if lane == candidate {
			return true
		}
	}
	return false
}

func IsKind(kind string) bool {
	for _, candidate := range KindOrder {
		if kind == candidate {
			return true
		}
	}
	return false
}

func LaneIndex(lane string) int {
	for i, candidate := range LaneOrder {
		if lane == candidate {
			return i
		}
	}
	return len(LaneOrder)
}

func validateText(field, value string, max int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if utf8.RuneCountInString(value) > max {
		return "", fmt.Errorf("%s cannot exceed %d characters", field, max)
	}
	return value, nil
}

func normalizeInput(input TaskInput, defaultSource string) (TaskInput, error) {
	var err error
	input.Title, err = validateText("title", input.Title, MaxTitleLength, true)
	if err != nil {
		return TaskInput{}, err
	}
	if input.Lane == "" {
		input.Lane = "backlog"
	}
	if !IsLane(input.Lane) {
		return TaskInput{}, fmt.Errorf("invalid lane %q", input.Lane)
	}
	if input.Kind == "" {
		input.Kind = "work"
	}
	if !IsKind(input.Kind) {
		return TaskInput{}, errors.New("kind must be one of work, followup, review, probe")
	}
	if input.Priority == 0 {
		input.Priority = 2
	}
	if input.Priority < 1 || input.Priority > 3 {
		return TaskInput{}, errors.New("priority must be 1, 2, or 3")
	}
	input.Project, err = validateText("project", input.Project, MaxProjectLength, false)
	if err != nil {
		return TaskInput{}, err
	}
	input.Tag, err = validateText("tag", input.Tag, MaxTagLength, false)
	if err != nil {
		return TaskInput{}, err
	}
	if strings.IndexFunc(input.Tag, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }) >= 0 {
		return TaskInput{}, errors.New("tag cannot contain whitespace")
	}
	input.Notes, err = validateText("notes", input.Notes, MaxNotesLength, false)
	if err != nil {
		return TaskInput{}, err
	}
	if input.Source == "" {
		input.Source = defaultSource
		if input.Source == "" {
			input.Source = defaultTaskSource
		}
	}
	input.Source, err = validateText("source", input.Source, MaxSourceLength, true)
	if err != nil {
		return TaskInput{}, err
	}
	if len(input.Subtasks) > MaxSubtasks {
		return TaskInput{}, fmt.Errorf("subtasks cannot contain more than %d items", MaxSubtasks)
	}
	for i := range input.Subtasks {
		input.Subtasks[i], err = normalizeSubtaskInput(input.Subtasks[i], input.Lane)
		if err != nil {
			return TaskInput{}, fmt.Errorf("subtasks[%d]: %w", i, err)
		}
	}
	return input, nil
}

func normalizeSubtaskInput(input SubtaskInput, parentLane string) (SubtaskInput, error) {
	var err error
	input.Title, err = validateText("subtask title", input.Title, MaxSubtaskTitle, true)
	if err != nil {
		return SubtaskInput{}, err
	}
	if input.Lane == "" {
		if input.Done {
			input.Lane = "done"
		} else {
			input.Lane = parentLane
		}
	}
	if !IsLane(input.Lane) {
		return SubtaskInput{}, fmt.Errorf("invalid subtask lane %q", input.Lane)
	}
	input.Done = input.Lane == "done"
	return input, nil
}
