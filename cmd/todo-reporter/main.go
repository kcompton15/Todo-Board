package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kcompton15/Todo-Board/internal/claudecli"
	"github.com/kcompton15/Todo-Board/internal/reportwriter"
)

const (
	pollInterval = 2 * time.Second
	maxBackoff   = 30 * time.Second
	maxReason    = 300
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "todo-reporter:", err)
		os.Exit(1)
	}
}

func run() error {
	defaultURL := os.Getenv("TODO_URL")
	if defaultURL == "" {
		defaultURL = "http://127.0.0.1:7337"
	}
	origin := flag.String("url", defaultURL, "loopback board origin")
	model := flag.String("model", claudecli.DefaultModel, "Claude model alias")
	budget := flag.Float64("budget", 0.50, "maximum USD per report request")
	once := flag.Bool("once", false, "process at most one job, then exit")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if *budget <= 0 || *budget > 5 {
		return errors.New("--budget must be greater than 0 and at most 5")
	}
	board, err := newBoard(*origin)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	worker := Worker{
		Board:  board,
		Writer: reportwriter.Writer{Model: claudecli.Client{Command: claudecli.ExecCommander{}}, ModelName: *model, Budget: *budget},
		Log:    log.New(os.Stderr, "todo-reporter: ", log.LstdFlags),
	}
	if *once {
		return worker.Once(ctx)
	}
	return worker.Loop(ctx)
}

type Writer interface {
	Write(ctx context.Context, kind string, evidence []byte) (reportwriter.Rendered, error)
}

type Worker struct {
	Board  *Board
	Writer Writer
	Log    *log.Logger
	Sleep  func(context.Context, time.Duration) error
}

// Once claims and processes at most one job.
func (w Worker) Once(ctx context.Context) error {
	job, ok, err := w.Board.Claim(ctx)
	if err != nil {
		return err
	}
	if !ok {
		w.Log.Print("no queued report jobs")
		return nil
	}
	reason, err := w.process(ctx, job)
	if err != nil {
		return err
	}
	if reason != "" {
		return fmt.Errorf("job %s failed: %s", job.ID, reason)
	}
	return nil
}

// Loop polls until ctx ends; board outages back off instead of exiting so launchd does not restart-loop.
func (w Worker) Loop(ctx context.Context) error {
	w.Log.Printf("polling %s every %s", w.Board.Origin, pollInterval)
	failures := 0
	for {
		job, ok, err := w.Board.Claim(ctx)
		delay := pollInterval
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil:
			failures++
			delay = backoff(failures)
			if failures == 1 || delay == maxBackoff && failures%10 == 0 {
				w.Log.Printf("claim failed (%d in a row): %v; retrying in %s", failures, err, delay)
			}
		case ok:
			if failures > 0 {
				w.Log.Printf("board reachable again after %d failed claims", failures)
			}
			failures = 0
			if _, err := w.process(ctx, job); err != nil {
				w.Log.Printf("job %s: %v", job.ID, err)
			}
			continue
		default:
			if failures > 0 {
				w.Log.Printf("board reachable again after %d failed claims", failures)
			}
			failures = 0
		}
		if err := w.sleep(ctx, delay); err != nil {
			return nil
		}
	}
}

func backoff(failures int) time.Duration {
	delay := pollInterval
	for i := 1; i < failures && delay < maxBackoff; i++ {
		delay *= 2
	}
	return min(delay, maxBackoff)
}

func (w Worker) sleep(ctx context.Context, delay time.Duration) error {
	if w.Sleep != nil {
		return w.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// process returns the reason it failed the job, or an error when the result could not be posted.
func (w Worker) process(ctx context.Context, job Job) (string, error) {
	started := time.Now()
	w.Log.Printf("job %s: %s %s..%s claimed", job.ID, job.Kind, job.Start, job.End)
	rendered, writeErr := w.Writer.Write(ctx, job.Kind, job.Evidence)
	// Results are posted on a fresh deadline so a shutdown mid-job still releases the job.
	postCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	elapsed := time.Since(started).Round(100 * time.Millisecond)
	if writeErr != nil {
		reason := shortReason(writeErr)
		if err := w.Board.Fail(postCtx, job.ID, reason); err != nil {
			return reason, fmt.Errorf("report failed (%s) and the failure could not be posted: %w", reason, err)
		}
		w.Log.Printf("job %s: failed after %s: %s", job.ID, elapsed, reason)
		return reason, nil
	}
	if err := w.Board.Complete(postCtx, job.ID, rendered.Markdown, rendered.CardCount); err != nil {
		return "", fmt.Errorf("complete: %w", err)
	}
	w.Log.Printf("job %s: done after %s, %d points citing %d cards, %d dropped", job.ID, elapsed, rendered.Points, rendered.CardCount, rendered.Dropped)
	return "", nil
}

func shortReason(err error) string {
	reason := strings.Join(strings.Fields(err.Error()), " ")
	if len([]rune(reason)) > maxReason {
		reason = string([]rune(reason)[:maxReason])
	}
	return reason
}

type Job struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	Start    string          `json:"start"`
	End      string          `json:"end"`
	Evidence json.RawMessage `json:"evidence"`
}

type Board struct {
	Origin string
	HTTP   *http.Client
}

func newBoard(origin string) (*Board, error) {
	origin = strings.TrimRight(origin, "/")
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" || parsed.Path != "" || parsed.RawQuery != "" || parsed.User != nil {
		return nil, errors.New("--url must be a loopback http origin")
	}
	host := parsed.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("--url must be a loopback http origin")
	}
	return &Board{Origin: origin, HTTP: &http.Client{Timeout: 15 * time.Second}}, nil
}

func (b *Board) Claim(ctx context.Context) (Job, bool, error) {
	var job Job
	status, err := b.post(ctx, "/api/reports/jobs/claim", nil, &job)
	if err != nil {
		return Job{}, false, err
	}
	if status == http.StatusNoContent {
		return Job{}, false, nil
	}
	if job.ID == "" || len(job.Evidence) == 0 {
		return Job{}, false, errors.New("claim response is missing the job id or evidence")
	}
	return job, true, nil
}

func (b *Board) Complete(ctx context.Context, id, markdown string, cardCount int) error {
	_, err := b.post(ctx, "/api/reports/jobs/"+url.PathEscape(id)+"/complete", map[string]any{"markdown": markdown, "cardCount": cardCount}, nil)
	return err
}

func (b *Board) Fail(ctx context.Context, id, reason string) error {
	_, err := b.post(ctx, "/api/reports/jobs/"+url.PathEscape(id)+"/fail", map[string]string{"error": reason}, nil)
	return err
}

func (b *Board) post(ctx context.Context, path string, body any, out any) (int, error) {
	payload := []byte("{}")
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		payload = encoded
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, b.Origin+path, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := b.HTTP.Do(request)
	if err != nil {
		return 0, fmt.Errorf("board unreachable: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, fmt.Errorf("read board response: %w", err)
	}
	if response.StatusCode == http.StatusNoContent {
		return response.StatusCode, nil
	}
	if response.StatusCode != http.StatusOK {
		return response.StatusCode, fmt.Errorf("board returned HTTP %d for %s", response.StatusCode, path)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return response.StatusCode, fmt.Errorf("decode board response for %s: %w", path, err)
		}
	}
	return response.StatusCode, nil
}
