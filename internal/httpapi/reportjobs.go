package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kcompton15/Todo-Board/internal/store"
)

const (
	reportJobLease       = 5 * time.Minute
	reportJobRetention   = time.Hour
	maxReportJobs        = 50
	maxReportMarkdown    = 32 * 1024
	maxReportJobErrorLen = 500
)

const (
	reportJobQueued  = "queued"
	reportJobRunning = "running"
	reportJobDone    = "done"
	reportJobFailed  = "failed"
)

var (
	errReportJobNotFound = errors.New("report job not found")
	errReportJobState    = errors.New("report job is not running")
	errReportJobsFull    = errors.New("too many report jobs in progress; try again shortly")
	errReportJobInternal = errors.New("could not create report job")
)

type reportJob struct {
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	Start          string    `json:"start"`
	End            string    `json:"end"`
	Projects       []string  `json:"projects,omitempty"`
	State          string    `json:"state"`
	Markdown       string    `json:"markdown"`
	DigestMarkdown string    `json:"digestMarkdown"`
	Error          string    `json:"error,omitempty"`
	CardCount      int       `json:"cardCount"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`

	evidence   store.Evidence
	leaseUntil time.Time
	requeued   bool
}

type claimedReportJob struct {
	reportJob
	Evidence store.Evidence `json:"evidence"`
}

// reportQueue holds disposable report drafts in memory; a restart drops them.
type reportQueue struct {
	mu          sync.Mutex
	jobs        []*reportJob
	lastClaimAt time.Time
	now         func() time.Time
	newID       func() (string, error)
}

func newReportQueue() *reportQueue {
	return &reportQueue{now: time.Now, newID: randomJobID}
}

func randomJobID() (string, error) {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate report job id: %w", err)
	}
	return "r" + hex.EncodeToString(buffer), nil
}

func (q *reportQueue) create(evidence store.Evidence) (reportJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	q.sweep(now)
	q.makeRoom()
	if len(q.jobs) >= maxReportJobs {
		return reportJob{}, errReportJobsFull
	}
	id, err := q.newID()
	if err != nil {
		return reportJob{}, fmt.Errorf("%w: %w", errReportJobInternal, err)
	}
	job := &reportJob{ID: id, Kind: evidence.Kind, Start: evidence.Start, End: evidence.End, Projects: evidence.Projects, State: reportJobQueued,
		DigestMarkdown: store.RenderDigest(evidence), CardCount: len(evidence.Cards), CreatedAt: now, UpdatedAt: now, evidence: evidence}
	q.jobs = append(q.jobs, job)
	return *job, nil
}

func (q *reportQueue) get(id string) (reportJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sweep(q.now())
	job := q.find(id)
	if job == nil {
		return reportJob{}, errReportJobNotFound
	}
	return *job, nil
}

func (q *reportQueue) claim() (claimedReportJob, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	q.sweep(now)
	q.lastClaimAt = now
	for _, job := range q.jobs {
		if job.State != reportJobQueued {
			continue
		}
		job.State = reportJobRunning
		job.leaseUntil = now.Add(reportJobLease)
		job.UpdatedAt = now
		return claimedReportJob{reportJob: *job, Evidence: job.evidence}, true
	}
	return claimedReportJob{}, false
}

func (q *reportQueue) complete(id, markdown string, cardCount *int) (reportJob, error) {
	markdown = strings.TrimSpace(markdown)
	if markdown == "" {
		return reportJob{}, errors.New("markdown is required")
	}
	if len(markdown) > maxReportMarkdown {
		return reportJob{}, fmt.Errorf("markdown cannot exceed %d bytes", maxReportMarkdown)
	}
	if cardCount != nil && *cardCount < 0 {
		return reportJob{}, errors.New("cardCount cannot be negative")
	}
	return q.finish(id, func(job *reportJob) {
		job.State = reportJobDone
		job.Markdown = markdown
		if cardCount != nil {
			job.CardCount = *cardCount
		}
	})
}

func (q *reportQueue) fail(id, message string) (reportJob, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return reportJob{}, errors.New("error is required")
	}
	return q.finish(id, func(job *reportJob) {
		job.State = reportJobFailed
		job.Error = truncateMessage(message, maxReportJobErrorLen)
	})
}

func (q *reportQueue) finish(id string, apply func(*reportJob)) (reportJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	q.sweep(now)
	job := q.find(id)
	if job == nil {
		return reportJob{}, errReportJobNotFound
	}
	if job.State != reportJobRunning {
		return reportJob{}, errReportJobState
	}
	apply(job)
	job.leaseUntil = time.Time{}
	job.UpdatedAt = now
	return *job, nil
}

func (q *reportQueue) workerStatus() (time.Time, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.lastClaimAt, !q.lastClaimAt.IsZero()
}

func (q *reportQueue) find(id string) *reportJob {
	for _, job := range q.jobs {
		if job.ID == id {
			return job
		}
	}
	return nil
}

// sweep requeues an expired lease once, fails it the second time, and drops old finished jobs.
func (q *reportQueue) sweep(now time.Time) {
	kept := q.jobs[:0]
	for _, job := range q.jobs {
		if job.State == reportJobRunning && !now.Before(job.leaseUntil) {
			job.leaseUntil = time.Time{}
			job.UpdatedAt = now
			if job.requeued {
				job.State = reportJobFailed
				job.Error = "the report worker stopped responding"
			} else {
				job.State = reportJobQueued
				job.requeued = true
			}
		}
		finished := job.State == reportJobDone || job.State == reportJobFailed
		if finished && now.Sub(job.UpdatedAt) >= reportJobRetention {
			continue
		}
		kept = append(kept, job)
	}
	q.replaceJobs(kept)
}

// makeRoom drops the oldest finished jobs so one more job fits under the cap.
func (q *reportQueue) makeRoom() {
	excess := len(q.jobs) - maxReportJobs + 1
	if excess <= 0 {
		return
	}
	kept := q.jobs[:0]
	for _, job := range q.jobs {
		if excess > 0 && (job.State == reportJobDone || job.State == reportJobFailed) {
			excess--
			continue
		}
		kept = append(kept, job)
	}
	q.replaceJobs(kept)
}

func (q *reportQueue) replaceJobs(kept []*reportJob) {
	for i := len(kept); i < len(q.jobs); i++ {
		q.jobs[i] = nil
	}
	q.jobs = kept
}

func truncateMessage(message string, limit int) string {
	if utf8.RuneCountInString(message) <= limit {
		return message
	}
	return string([]rune(message)[:limit])
}

func (s *Server) createReportJob(w http.ResponseWriter, r *http.Request) {
	var request store.ReportRequest
	if !decodeRequest(w, r, &request) {
		return
	}
	evidence, err := s.store.ReportEvidence(request)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	job, err := s.reports.create(evidence)
	if err != nil {
		writeReportJobError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) getReportJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.reports.get(r.PathValue("id"))
	if err != nil {
		writeReportJobError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) claimReportJob(w http.ResponseWriter, _ *http.Request) {
	job, ok := s.reports.claim()
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) completeReportJob(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Markdown  string `json:"markdown"`
		CardCount *int   `json:"cardCount,omitempty"`
	}
	if !decodeRequest(w, r, &body) {
		return
	}
	job, err := s.reports.complete(r.PathValue("id"), body.Markdown, body.CardCount)
	if err != nil {
		writeReportJobError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) failReportJob(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Error string `json:"error"`
	}
	if !decodeRequest(w, r, &body) {
		return
	}
	job, err := s.reports.fail(r.PathValue("id"), body.Error)
	if err != nil {
		writeReportJobError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) reportWorker(w http.ResponseWriter, _ *http.Request) {
	status := map[string]any{"lastClaimAt": nil}
	if at, ok := s.reports.workerStatus(); ok {
		status["lastClaimAt"] = at
	}
	writeJSON(w, http.StatusOK, status)
}

func writeReportJobError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errReportJobNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errReportJobState):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errReportJobsFull):
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, errReportJobInternal):
		writeError(w, http.StatusInternalServerError, errReportJobInternal.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}
