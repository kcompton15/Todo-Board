package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kcompton15/Todo-Board/internal/httpapi"
	"github.com/kcompton15/Todo-Board/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type noProvider struct{}

func (noProvider) Issues(context.Context, []string) (map[string]IssueState, error) {
	return nil, errors.New("unavailable")
}
func (noProvider) MR(context.Context, string, int) (MRState, error) {
	return MRState{}, errors.New("unavailable")
}
func (noProvider) SearchMRs(context.Context, string) ([]MRState, error) {
	return nil, errors.New("unavailable")
}
func (noProvider) ReviewActivity(context.Context, string, int) (ReviewActivity, error) {
	return ReviewActivity{}, errors.New("unavailable")
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRunnerDryRunCrashAndLostResponse(t *testing.T) {
	s, _ := store.New(t.TempDir())
	tasks, _ := s.CreateMany([]store.TaskInput{{Title: "delivered", Kind: "review"}}, "")
	task := tasks[0]
	s.AddWorkLog(store.WorkLogInput{TaskID: task.ID, Kind: store.WorkLogReviewDelivered, Text: "final report"}, store.Mutation{})
	server := httptest.NewServer(httpapi.New(s, httpapi.NewHub(), nil, nil).Handler())
	defer server.Close()
	board, _ := NewBoard(server.URL, nil)
	runner := Runner{Board: board, GitLab: noProvider{}, Jira: noProvider{}}
	path := filepath.Join(t.TempDir(), "state.json")
	before := s.History(store.HistoryFilter{})
	dry, err := runner.Run(context.Background(), RunOptions{DryRun: true, NoLLM: true, StatePath: path})
	if err != nil || len(dry.Plan.Envelopes) != 1 || !reflect.DeepEqual(before, s.History(store.HistoryFilter{})) {
		t.Fatal(dry, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry run wrote state")
	}
	// Deliver the request, discard its successful response, then recover via receipt GET.
	posts := 0
	board.Client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		res, err := http.DefaultTransport.RoundTrip(req)
		if req.Method == "POST" && err == nil {
			posts++
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			return nil, errors.New("response lost")
		}
		return res, err
	})
	replay, err := board.Apply(context.Background(), dry.Plan.Envelopes[0])
	if err != nil || !replay || posts != 1 {
		t.Fatal(replay, posts, err)
	}
	// Crash before state checkpoint: missing state cannot duplicate committed actions.
	again, err := runner.Run(context.Background(), RunOptions{NoLLM: true, StatePath: path})
	if err != nil || again.Changed != 0 || len(again.Plan.Envelopes) != 0 || !again.CursorAdvanced {
		t.Fatal(again, err)
	}
	if _, err := LoadState(path, "http://127.0.0.1:1"); err == nil {
		t.Fatal("foreign state accepted")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("state permissions", info.Mode())
	}
	data, _ := json.Marshal(again)
	if len(data) == 0 {
		t.Fatal("empty report")
	}
}

func TestCorruptStatePreservedAndExclusiveLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	for _, data := range []string{`{}`, `broken`, `{"version":2,"origin":"http://localhost:7337"}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadState(path, "http://localhost:7337"); err == nil {
			t.Fatal("corrupt state accepted", data)
		}
		got, _ := os.ReadFile(path)
		if string(got) != data {
			t.Fatal("corrupt state modified")
		}
	}
	lockPath := filepath.Join(t.TempDir(), "lock")
	unlock, err := Lock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if release, err := Lock(lockPath); err == nil {
		release()
		t.Fatal("concurrent lock accepted")
	}
}

func TestRunnerPerCardHistoryConflict(t *testing.T) {
	s, _ := store.New(t.TempDir())
	tasks, _ := s.CreateMany([]store.TaskInput{{Title: "a", Kind: "review"}, {Title: "b", Kind: "review"}}, "")
	for _, task := range tasks {
		s.AddWorkLog(store.WorkLogInput{TaskID: task.ID, Kind: store.WorkLogReviewDelivered, Text: "final"}, store.Mutation{})
	}
	base := httpapi.New(s, httpapi.NewHub(), nil, nil).Handler()
	once := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && !once {
			once = true
			s.AddWorkLog(store.WorkLogInput{TaskID: tasks[0].ID, Kind: store.WorkLogBlocker, Text: "pause"}, store.Mutation{})
		}
		base.ServeHTTP(w, r)
	}))
	defer server.Close()
	board, _ := NewBoard(server.URL, nil)
	runner := Runner{Board: board, GitLab: noProvider{}, Jira: noProvider{}}
	result, err := runner.Run(context.Background(), RunOptions{NoLLM: true, StatePath: filepath.Join(t.TempDir(), "state.json")})
	if err != nil || result.Conflicted != 1 || result.Changed != 1 || result.CursorAdvanced {
		t.Fatal(result, err)
	}
}

func TestModelFailureAndConcurrentHistoryAfterDeterministicCommit(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(fmt.Sprint(race), func(t *testing.T) {
			s, _ := store.New(t.TempDir())
			tasks, _ := s.CreateMany([]store.TaskInput{{Title: "delivered", Kind: "review"}, {Title: "remaining", Kind: "work"}}, "")
			delivered, remaining := tasks[0], tasks[1]
			if _, err := s.AddWorkLog(store.WorkLogInput{TaskID: delivered.ID, Kind: store.WorkLogReviewDelivered, Text: "final"}, store.Mutation{}); err != nil {
				t.Fatal(err)
			}
			entry, err := s.AddWorkLog(store.WorkLogInput{TaskID: remaining.ID, Kind: store.WorkLogProgress, Text: "investigated"}, store.Mutation{})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(httpapi.New(s, httpapi.NewHub(), nil, nil).Handler())
			defer server.Close()
			board, _ := NewBoard(server.URL, nil)
			model := assistantFunc(func(_ context.Context, input []byte) (Proposals, error) {
				got, _ := s.Get(delivered.ID)
				if got.Lane != "done" {
					t.Fatal("model ran before deterministic commit")
				}
				if bytes.Contains(input, []byte(`"id":"`+delivered.ID+`"`)) {
					t.Fatal("model bundle not reread")
				}
				if !race {
					return Proposals{}, errors.New("fake model failure")
				}
				if _, err := s.AddWorkLog(store.WorkLogInput{TaskID: remaining.ID, Kind: store.WorkLogBlocker, Text: "new pause"}, store.Mutation{}); err != nil {
					t.Fatal(err)
				}
				return Proposals{[]Proposal{{TaskID: remaining.ID, Kind: "agent_context", Value: "suggested", EvidenceIDs: []string{entry.ID}}}}, nil
			})
			runner := Runner{Board: board, GitLab: noProvider{}, Jira: noProvider{}, Model: model}
			result, err := runner.Run(context.Background(), RunOptions{StatePath: filepath.Join(t.TempDir(), "state.json")})
			if err != nil || result.Changed != 1 || result.CursorAdvanced {
				t.Fatal(result, err)
			}
			if race && result.Conflicted != 1 {
				t.Fatal("model history race not rejected", result)
			}
			if !race && len(result.Errors) != 1 {
				t.Fatal("model failure hidden", result)
			}
			got, _ := s.Get(remaining.ID)
			if got.AgentContext != "" {
				t.Fatal("stale memo committed")
			}
		})
	}
}
