package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kcompton15/Todo-Board/internal/claudecli"
	"github.com/kcompton15/Todo-Board/internal/reportwriter"
)

const evidenceJSON = `{"kind":"weekly","start":"2026-10-05","end":"2026-10-11","cards":[{"number":110,"title":"Ship export queue","project":"Billing","kind":"work","lane":"done","doneInWindow":true,"observed":["web!140 merged"]},{"number":133,"title":"Export QA","project":"Billing","kind":"work","lane":"doing","doneInWindow":false}],"openBlockers":[],"truncated":false,"partialLegacyHistory":false}`

type fakeClaude struct {
	result string
	err    error
	stdin  []byte
	args   []string
}

func (f *fakeClaude) Run(_ context.Context, c claudecli.Command) ([]byte, error) {
	for _, value := range c.Env {
		if strings.HasPrefix(value, "TODO_SECRET=") {
			return nil, errors.New("credential inherited")
		}
	}
	switch {
	case len(c.Args) == 1 && c.Args[0] == "--version":
		return []byte(claudecli.VerifiedVersion + " (Claude Code)"), nil
	case len(c.Args) == 1 && c.Args[0] == "--help":
		return []byte("--safe-mode --max-budget-usd --json-schema --strict-mcp-config --debug-file"), nil
	}
	if i := slices.Index(c.Args, "--debug-file"); i >= 0 {
		if err := os.WriteFile(c.Args[i+1], []byte("project memory is off\nFound 0 total hooks in registry"), 0600); err != nil {
			return nil, err
		}
		return []byte(`{"type":"system","subtype":"init","tools":[],"mcp_servers":[],"skills":[]}` + "\n" + `{"type":"result","subtype":"success","is_error":false,"result":"ISOLATION_OK"}`), nil
	}
	f.stdin, f.args = c.Stdin, c.Args
	if f.err != nil {
		return nil, f.err
	}
	return []byte(`[{"type":"system","subtype":"init","tools":["StructuredOutput"],"mcp_servers":[],"skills":[]},{"type":"result","subtype":"success","is_error":false,"structured_output":` + f.result + `}]`), nil
}

type fakeBoard struct {
	mu       sync.Mutex
	queued   bool
	complete map[string]any
	fail     string
}

func (b *fakeBoard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch r.URL.Path {
	case "/api/reports/jobs/claim":
		if !b.queued {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		b.queued = false
		if _, err := io.WriteString(w, `{"id":"r1","kind":"weekly","start":"2026-10-05","end":"2026-10-11","state":"running","evidence":`+evidenceJSON+`}`); err != nil {
			panic(err)
		}
	case "/api/reports/jobs/r1/complete":
		if err := json.Unmarshal(body, &b.complete); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := io.WriteString(w, `{}`); err != nil {
			panic(err)
		}
	case "/api/reports/jobs/r1/fail":
		var payload struct{ Error string }
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b.fail = payload.Error
		if _, err := io.WriteString(w, `{}`); err != nil {
			panic(err)
		}
	default:
		http.NotFound(w, r)
	}
}

func runOnce(t *testing.T, claude *fakeClaude) (*fakeBoard, string, error) {
	t.Helper()
	t.Setenv("TODO_SECRET", "never-pass")
	board := &fakeBoard{queued: true}
	server := httptest.NewServer(board)
	t.Cleanup(server.Close)
	client, err := newBoard(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	worker := Worker{
		Board:  client,
		Writer: reportwriter.Writer{Model: claudecli.Client{Command: claude}, ModelName: "sonnet", Budget: 0.5},
		Log:    log.New(&logs, "", 0),
	}
	err = worker.Once(context.Background())
	return board, logs.String(), err
}

func TestOnceCompletesWithRenderedMarkdown(t *testing.T) {
	claude := &fakeClaude{result: `{"points":[{"text":"Export queue shipped","cards":[110],"details":["web!140 merged"]},{"text":"QA under way","cards":[133],"details":[]}],"blockers":[]}`}
	board, logs, err := runOnce(t, claude)
	if err != nil {
		t.Fatal(err, logs)
	}
	want := "- Export queue shipped (#110)\n  - web!140 merged\n- QA under way (#133)"
	if board.complete["markdown"] != want || board.complete["cardCount"] != float64(2) || board.fail != "" {
		t.Fatalf("%+v %q", board.complete, board.fail)
	}
	if string(claude.stdin) != evidenceJSON {
		t.Fatal("evidence was not sent verbatim on stdin")
	}
	for _, pair := range [][2]string{{"--model", "sonnet"}, {"--max-budget-usd", "0.50"}, {"--tools", ""}} {
		i := slices.Index(claude.args, pair[0])
		if i < 0 || claude.args[i+1] != pair[1] {
			t.Fatalf("%s not %q in %q", pair[0], pair[1], claude.args)
		}
	}
	if strings.Contains(logs, "Export queue") || strings.Contains(logs, "never-pass") {
		t.Fatalf("logs leaked content:\n%s", logs)
	}
}

func TestOnceDropsUnknownCardCitations(t *testing.T) {
	claude := &fakeClaude{result: `{"points":[{"text":"Export queue shipped","cards":[110],"details":[]},{"text":"Invented win","cards":[110,4242],"details":[]}],"blockers":[{"text":"Invented blocker","cards":[7]}]}`}
	board, logs, err := runOnce(t, claude)
	if err != nil {
		t.Fatal(err, logs)
	}
	if board.complete["markdown"] != "- Export queue shipped (#110)" || !strings.Contains(logs, "2 dropped") {
		t.Fatalf("%+v\n%s", board.complete, logs)
	}
}

func TestOnceFailsJob(t *testing.T) {
	for name, claude := range map[string]*fakeClaude{
		"malformed output":   {result: `{"points":"lots","blockers":[]}`},
		"all points invalid": {result: `{"points":[{"text":"Invented","cards":[9],"details":[]}],"blockers":[]}`},
		"model error":        {err: errors.New("exit status 1: secret stderr")},
		"model timeout":      {err: context.DeadlineExceeded},
	} {
		t.Run(name, func(t *testing.T) {
			board, logs, err := runOnce(t, claude)
			if err == nil || board.complete != nil || board.fail == "" {
				t.Fatalf("%v %+v %q\n%s", err, board.complete, board.fail, logs)
			}
			if strings.Contains(board.fail, "secret stderr") {
				t.Fatal("command error text reached the job")
			}
		})
	}
}

func TestOnceWithNoJobExitsCleanly(t *testing.T) {
	server := httptest.NewServer(&fakeBoard{})
	t.Cleanup(server.Close)
	client, err := newBoard(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	worker := Worker{Board: client, Writer: reportwriter.Writer{}, Log: log.New(&logs, "", 0)}
	if err := worker.Once(context.Background()); err != nil || !strings.Contains(logs.String(), "no queued") {
		t.Fatal(err, logs.String())
	}
}

func TestLoopBacksOffWhileBoardIsDown(t *testing.T) {
	client, err := newBoard("http://127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	delays := []time.Duration{}
	var logs bytes.Buffer
	worker := Worker{Board: client, Writer: reportwriter.Writer{}, Log: log.New(&logs, "", 0), Sleep: func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		if len(delays) == 7 {
			cancel()
			return context.Canceled
		}
		return nil
	}}
	if err := worker.Loop(ctx); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}
	if !slices.Equal(delays, want) {
		t.Fatalf("delays %v", delays)
	}
	if strings.Count(logs.String(), "claim failed") != 1 {
		t.Fatalf("log spam:\n%s", logs.String())
	}
}

func TestNewBoardRequiresLoopback(t *testing.T) {
	for _, origin := range []string{"http://127.0.0.1:7337", "http://localhost:7337/", "http://[::1]:7337"} {
		if _, err := newBoard(origin); err != nil {
			t.Fatalf("%s: %v", origin, err)
		}
	}
	for _, origin := range []string{"https://127.0.0.1:7337", "http://example.com", "http://10.0.0.1:7337", "http://127.0.0.1:7337/api", "http://u:p@127.0.0.1"} {
		if _, err := newBoard(origin); err == nil {
			t.Fatalf("%s accepted", origin)
		}
	}
}
