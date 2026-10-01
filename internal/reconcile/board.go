package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kcompton15/Todo-Board/internal/store"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const maxResponse = 16 << 20

type APIError struct {
	Status int
	Code   string
}

func (e *APIError) Error() string { return fmt.Sprintf("board HTTP %d (%s)", e.Status, e.Code) }

type Board struct {
	Origin string
	Client *http.Client
}

func NewBoard(origin string, client *http.Client) (*Board, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("board must be a loopback HTTP origin")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("board host must be loopback")
	}
	c := http.Client{Timeout: 10 * time.Second}
	if client != nil {
		c = *client
		if c.Timeout <= 0 {
			c.Timeout = 10 * time.Second
		}
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("board redirects are prohibited") }
	return &Board{Origin: "http://" + u.Host, Client: &c}, nil
}
func (b *Board) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, b.Origin+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("X-Actor", "board-reconciler")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := b.Client.Do(request)
	if err != nil {
		return errors.New("board request failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return errors.New("board response read failed")
	}
	if len(data) > maxResponse {
		return errors.New("board response exceeds limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var detail struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(data, &detail)
		return &APIError{Status: response.StatusCode, Code: detail.Code}
	}
	if err := json.Unmarshal(data, output); err != nil {
		return errors.New("invalid board JSON response")
	}
	return nil
}
func (b *Board) Tasks(ctx context.Context) ([]store.Task, error) {
	var out struct {
		Tasks []store.Task `json:"tasks"`
	}
	if err := b.request(ctx, "GET", "/api/tasks", nil, &out); err != nil {
		return nil, err
	}
	if out.Tasks == nil {
		return nil, errors.New("missing tasks array")
	}
	seen := map[string]bool{}
	for _, t := range out.Tasks {
		if t.ID == "" || t.Revision == 0 || seen[t.ID] {
			return nil, errors.New("invalid task identity/revision")
		}
		seen[t.ID] = true
	}
	return out.Tasks, nil
}
func (b *Board) History(ctx context.Context, id string) (CardHistory, error) {
	result := CardHistory{}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		var out store.HistoryPage
		path := "/api/history?limit=100"
		if id != "" {
			path += "&taskId=" + url.QueryEscape(id)
		}
		if cursor != "" {
			path += "&before=" + url.QueryEscape(cursor)
		}
		if err := b.request(ctx, "GET", path, nil, &out); err != nil {
			return result, err
		}
		for _, record := range out.Records {
			if record.ID == "" || seen[record.ID] || (id != "" && record.TaskID != id) {
				return result, errors.New("invalid history identity or pagination")
			}
			seen[record.ID] = true
			result.Records = append(result.Records, record)
		}
		if page == 0 && len(out.Records) > 0 {
			result.Head = out.Records[0].ID
		}
		if out.NextBefore == "" {
			result.Complete = !out.PartialLegacyHistory
			for _, record := range result.Records {
				if id != "" && record.Kind == "task_created" && !record.Partial {
					result.Complete = true
				}
			}
			return result, nil
		}
		if out.NextBefore == cursor || len(out.Records) == 0 || out.NextBefore != out.Records[len(out.Records)-1].ID {
			return result, errors.New("invalid history cursor")
		}
		cursor = out.NextBefore
	}
	return result, errors.New("history exceeds 100 pages; completion withheld")
}
func (b *Board) Apply(ctx context.Context, envelope Envelope) (bool, error) {
	var out struct {
		Applied        int          `json:"applied"`
		AlreadyApplied bool         `json:"alreadyApplied"`
		Tasks          []store.Task `json:"tasks"`
	}
	err := b.request(ctx, "POST", "/api/ops", envelope.Batch, &out)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			return false, err
		}
		// A response lost after commit is resolved by its durable receipt, never blind retry.
		history, readErr := b.History(ctx, envelope.TaskID)
		if readErr != nil {
			return false, err
		}
		actionID := ""
		hash, hashErr := store.ActionHash(envelope.Batch)
		if hashErr != nil {
			return false, hashErr
		}
		for _, op := range envelope.Batch.Ops {
			if op.WorkLog != nil {
				actionID = op.WorkLog.ActionID
			}
		}
		for _, r := range history.Records {
			if r.WorkLog != nil && r.WorkLog.ActionID == actionID && actionID != "" && r.WorkLog.ActionHash == hash {
				return true, nil
			}
		}
		return false, err
	}
	expected := len(envelope.Batch.Ops)
	if out.AlreadyApplied {
		expected = 0
	}
	if out.Applied != expected || out.Tasks == nil {
		return false, errors.New("incomplete apply response")
	}
	found := false
	for _, task := range out.Tasks {
		if task.ID == envelope.TaskID {
			found = true
		}
	}
	if !found {
		return false, errors.New("applied card missing from response")
	}
	return out.AlreadyApplied, nil
}
