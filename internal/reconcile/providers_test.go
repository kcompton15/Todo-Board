package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type commandFunc func(context.Context, Command) ([]byte, error)

func (f commandFunc) Run(c context.Context, in Command) ([]byte, error) { return f(c, in) }

func TestGitLabPaginationIdentityAndReview(t *testing.T) {
	g := &GitLabClient{Username: "reviewer.one", Group: "team", Command: commandFunc(func(_ context.Context, c Command) ([]byte, error) {
		if c.Args[1] != "--method" || c.Args[2] != "GET" {
			t.Fatal("non-read operation", c.Args)
		}
		path := c.Args[len(c.Args)-1]
		switch {
		case strings.Contains(path, "users?"):
			return []byte(`[{"id":42,"username":"reviewer.one"}]`), nil
		case strings.Contains(path, "/approvals"):
			return []byte(`{"approved_by":[{"user":{"id":42}}]}`), nil
		case strings.Contains(path, "/discussions"):
			return []byte(`[{"id":"a","notes":[{"id":1,"author":{"id":99},"created_at":"2026-09-29T10:00:00Z","body":"unrelated"}]}]
[{"id":"b","notes":[{"id":2,"author":{"id":42},"created_at":"2026-09-29T11:00:00Z","updated_at":"2026-09-29T12:00:00Z","type":"DiffNote","body":"This drops the required invoice ID on update.","resolved":false},{"id":3,"author":{"id":42},"created_at":"2026-09-29T11:01:00Z","system":true,"body":"approved this merge request"}]}]`), nil
		default:
			return []byte(`[{"iid":1,"state":"opened","title":"Draft: PROJ-12 fix","web_url":"https://gitlab.com/a/api/-/merge_requests/1"}]
[{"iid":2,"state":"merged","title":"WIP: PROJ-12 web","web_url":"https://gitlab.com/a/web/-/merge_requests/2"},{"iid":3,"state":"merged","title":"PROJ-123 false","web_url":"https://gitlab.com/a/web/-/merge_requests/3"}]`), nil
		}
	})}
	matches, err := g.SearchMRs(context.Background(), "PROJ-12")
	if err != nil || len(matches) != 2 {
		t.Fatal(matches, err)
	}
	activity, err := g.ReviewActivity(context.Background(), "a/api", 1)
	if err != nil || !activity.Complete || len(activity.Events) != 4 {
		t.Fatal(activity, err)
	}
	if activity.Events[1].CreatedAt.Hour() != 11 || !substantive(activity.Events[1]) || activity.Events[3].ID != "note:3" {
		t.Fatal(activity)
	}
	g.Command = commandFunc(func(context.Context, Command) ([]byte, error) {
		return []byte(`[{"iid":9,"state":"merged","web_url":"https://gitlab.com/a/api/-/merge_requests/9"}]`), nil
	})
	if _, err := g.MR(context.Background(), "a/api", 1); err == nil {
		t.Fatal("wrong identity accepted")
	}
	if _, err := decodePages[int]([]byte(`[1] garbage`)); err == nil {
		t.Fatal("partial malformed pages accepted")
	}
	g.Command = commandFunc(func(context.Context, Command) ([]byte, error) { return nil, errors.New("outage") })
	if a, err := g.ReviewActivity(context.Background(), "a/api", 1); err == nil || a.Complete {
		t.Fatal("outage marked complete")
	}
}

func TestJiraPaginationRetryAndMalformed(t *testing.T) {
	calls := 0
	bad := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		user, token, ok := r.BasicAuth()
		if !ok || user != "owner@example.com" || token != "test-only" || r.Method != "GET" {
			t.Error("auth/method")
		}
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		if bad {
			fmt.Fprint(w, `{"issues":[],"nextPageToken":"repeat"}`)
			return
		}
		key, last, next := "PROJ-1", false, "next"
		if r.URL.Query().Get("nextPageToken") == "next" {
			key, last, next = "PROJ-2", true, ""
		}
		json.NewEncoder(w).Encode(map[string]any{"issues": []any{map[string]any{"key": key, "fields": map[string]any{"status": map[string]any{"name": "Done", "statusCategory": map[string]string{"key": "done"}}}}}, "isLast": last, "nextPageToken": next})
	}))
	defer server.Close()
	j := JiraClient{Origin: server.URL, Email: "owner@example.com", Token: func(context.Context) (string, error) { return "test-only", nil }}
	issues, err := j.Issues(context.Background(), []string{"PROJ-1", "PROJ-2"})
	if err != nil || len(issues) != 2 || calls != 3 {
		t.Fatal(issues, calls, err)
	}
	bad = true
	if _, err := j.Issues(context.Background(), []string{"PROJ-1"}); err == nil {
		t.Fatal("repeated cursor accepted")
	}
}

func TestReviewAmbiguity(t *testing.T) {
	for _, text := range []string{"No findings so far.", "Review complete for the first file; remaining files tomorrow.", "Not reviewed yet, will review tomorrow.", "Will review complete changes tomorrow.", "I have not reviewed this.", "Could this be a bug?", "Thanks for the update"} {
		if substantive(ReviewEvent{Body: text, Diff: true}) {
			t.Errorf("logistics counted: %q", text)
		}
	}
}
