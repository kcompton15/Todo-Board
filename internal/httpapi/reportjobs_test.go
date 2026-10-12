package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kcompton15/Todo-Board/internal/store"
)

type fakeClock struct{ at time.Time }

func (c *fakeClock) now() time.Time { return c.at }

func newReportTestServer(t *testing.T) (*httptest.Server, *Server, *store.Store, *fakeClock) {
	t.Helper()
	taskStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	api := New(taskStore, NewHub(), nil, []byte("<html>board</html>"))
	clock := &fakeClock{at: time.Date(2026, 10, 12, 15, 0, 0, 0, time.UTC)}
	api.reports.now = clock.now
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	return server, api, taskStore, clock
}

func TestReportJobLifecycle(t *testing.T) {
	server, _, taskStore, _ := newReportTestServer(t)
	tasks, err := taskStore.CreateMany([]store.TaskInput{{Title: "Reported card", Project: "Tools"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := taskStore.AddWorkLog(store.WorkLogInput{TaskID: tasks[0].ID, Kind: store.WorkLogProgress, Text: "observed ABC-1 Done\nshipped the thing", OccurredAt: time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC)}, store.Mutation{Actor: "test"}); err != nil {
		t.Fatal(err)
	}

	var worker map[string]any
	decodeResponse(t, request(t, server.URL+"/api/reports/worker", http.MethodGet, "", ""), &worker)
	if worker["lastClaimAt"] != nil {
		t.Fatalf("worker before any claim = %v", worker)
	}

	response := request(t, server.URL+"/api/reports/jobs", http.MethodPost, `{"kind":"weekly","start":"2026-10-05","end":"2026-10-11"}`, "application/json")
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("create returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	var created reportJob
	decodeResponse(t, response, &created)
	if created.ID == "" || created.State != reportJobQueued || created.CardCount != 1 || !strings.Contains(created.DigestMarkdown, "#1 Reported card") {
		t.Fatalf("created = %+v", created)
	}

	response = request(t, server.URL+"/api/reports/jobs/claim", http.MethodPost, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("claim returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	var claimed struct {
		reportJob
		Evidence store.Evidence `json:"evidence"`
	}
	decodeResponse(t, response, &claimed)
	if claimed.ID != created.ID || claimed.State != reportJobRunning || len(claimed.Evidence.Cards) != 1 || claimed.Evidence.Cards[0].Observed[0] != "ABC-1 Done" || claimed.DigestMarkdown == "" {
		t.Fatalf("claimed = %+v", claimed)
	}

	response = request(t, server.URL+"/api/reports/jobs/claim", http.MethodPost, "", "")
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("empty claim returned %d", response.StatusCode)
	}
	closeResponse(t, response)
	decodeResponse(t, request(t, server.URL+"/api/reports/worker", http.MethodGet, "", ""), &worker)
	if worker["lastClaimAt"] != "2026-10-12T15:00:00Z" {
		t.Fatalf("worker after claim = %v", worker)
	}

	response = request(t, server.URL+"/api/reports/jobs/"+created.ID+"/complete", http.MethodPost, `{"markdown":"- Shipped the thing (#1)","cardCount":1}`, "application/json")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("complete returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	closeResponse(t, response)
	var done reportJob
	decodeResponse(t, request(t, server.URL+"/api/reports/jobs/"+created.ID, http.MethodGet, "", ""), &done)
	if done.State != reportJobDone || done.Markdown != "- Shipped the thing (#1)" || done.CardCount != 1 {
		t.Fatalf("done = %+v", done)
	}

	for _, check := range []struct {
		path, body string
		status     int
	}{
		{"/api/reports/jobs/" + created.ID + "/complete", `{"markdown":"again"}`, http.StatusConflict},
		{"/api/reports/jobs/" + created.ID + "/fail", `{"error":"late"}`, http.StatusConflict},
		{"/api/reports/jobs/rmissing/fail", `{"error":"nope"}`, http.StatusNotFound},
		{"/api/reports/jobs", `{"kind":"monthly","start":"2026-10-05","end":"2026-10-11"}`, http.StatusBadRequest},
		{"/api/reports/jobs", `{"kind":"weekly","start":"2026-10-11","end":"2026-10-05"}`, http.StatusBadRequest},
	} {
		response := request(t, server.URL+check.path, http.MethodPost, check.body, "application/json")
		if response.StatusCode != check.status {
			t.Fatalf("%s %s returned %d, want %d: %s", check.path, check.body, response.StatusCode, check.status, readResponseBody(t, response))
		}
		closeResponse(t, response)
	}
	response = request(t, server.URL+"/api/reports/jobs/rmissing", http.MethodGet, "", "")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("missing job returned %d", response.StatusCode)
	}
	closeResponse(t, response)
}

func TestReportJobFailAndValidation(t *testing.T) {
	server, api, _, _ := newReportTestServer(t)
	job, err := api.reports.create(store.Evidence{Kind: "standup", Start: "2026-10-12", End: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := api.reports.claim(); !ok {
		t.Fatal("nothing to claim")
	}
	big := strings.Repeat("a", maxReportMarkdown+1)
	for _, body := range []string{`{"markdown":"  "}`, `{"markdown":"` + big + `"}`, `{"markdown":"ok","cardCount":-1}`} {
		response := request(t, server.URL+"/api/reports/jobs/"+job.ID+"/complete", http.MethodPost, body, "application/json")
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("complete %.40s returned %d", body, response.StatusCode)
		}
		closeResponse(t, response)
	}
	response := request(t, server.URL+"/api/reports/jobs/"+job.ID+"/fail", http.MethodPost, `{"error":"`+strings.Repeat("e", 700)+`"}`, "application/json")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("fail returned %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	var failed reportJob
	decodeResponse(t, response, &failed)
	if failed.State != reportJobFailed || len(failed.Error) != maxReportJobErrorLen || failed.Markdown != "" || failed.DigestMarkdown == "" {
		t.Fatalf("failed = %+v", failed)
	}
}

func TestReportJobLeaseExpiresOnceThenFails(t *testing.T) {
	_, api, _, clock := newReportTestServer(t)
	queue := api.reports
	job, err := queue.create(store.Evidence{Kind: "weekly", Start: "2026-10-05", End: "2026-10-11"})
	if err != nil {
		t.Fatal(err)
	}
	if claimed, ok := queue.claim(); !ok || claimed.ID != job.ID {
		t.Fatal("first claim failed")
	}
	clock.at = clock.at.Add(reportJobLease - time.Second)
	if got, err := queue.get(job.ID); err != nil || got.State != reportJobRunning {
		t.Fatalf("before lease expiry = %+v, %v", got, err)
	}
	clock.at = clock.at.Add(time.Second)
	if got, err := queue.get(job.ID); err != nil || got.State != reportJobQueued {
		t.Fatalf("after first expiry = %+v, %v", got, err)
	}
	if _, err := queue.complete(job.ID, "stale worker", nil); !errors.Is(err, errReportJobState) {
		t.Fatalf("stale complete err = %v", err)
	}
	if claimed, ok := queue.claim(); !ok || claimed.ID != job.ID {
		t.Fatal("requeued job was not claimable")
	}
	clock.at = clock.at.Add(reportJobLease)
	got, err := queue.get(job.ID)
	if err != nil || got.State != reportJobFailed || got.Error == "" {
		t.Fatalf("after second expiry = %+v, %v", got, err)
	}
	if _, ok := queue.claim(); ok {
		t.Fatal("failed job was claimed again")
	}
}

func TestReportJobCapsAndRetention(t *testing.T) {
	_, api, _, clock := newReportTestServer(t)
	queue := api.reports
	evidence := store.Evidence{Kind: "standup", Start: "2026-10-12", End: "2026-10-12"}
	ids := make([]string, 0, maxReportJobs)
	for i := 0; i < maxReportJobs; i++ {
		job, err := queue.create(evidence)
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		ids = append(ids, job.ID)
	}
	if _, err := queue.create(evidence); !errors.Is(err, errReportJobsFull) {
		t.Fatalf("over cap err = %v", err)
	}

	for i := 0; i < 2; i++ {
		claimed, ok := queue.claim()
		if !ok || claimed.ID != ids[i] {
			t.Fatalf("claim %d = %+v", i, claimed)
		}
		if _, err := queue.fail(claimed.ID, fmt.Sprintf("failure %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := queue.create(evidence); err != nil {
		t.Fatalf("create after a job finished: %v", err)
	}
	if _, err := queue.get(ids[0]); !errors.Is(err, errReportJobNotFound) {
		t.Fatalf("oldest finished job should make room, got %v", err)
	}
	if _, err := queue.get(ids[1]); err != nil {
		t.Fatalf("second finished job dropped early: %v", err)
	}

	clock.at = clock.at.Add(reportJobRetention)
	if _, err := queue.get(ids[1]); !errors.Is(err, errReportJobNotFound) {
		t.Fatalf("finished job outlived retention: %v", err)
	}
	if _, err := queue.get(ids[2]); err != nil {
		t.Fatalf("queued job dropped by retention: %v", err)
	}
}
