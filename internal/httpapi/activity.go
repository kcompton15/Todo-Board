package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kcompton15/Todo-Board/internal/store"
)

func mutationHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			actor := strings.TrimSpace(r.Header.Get("X-Actor"))
			if utf8.RuneCountInString(actor) > 80 || strings.IndexFunc(actor, unicode.IsControl) >= 0 {
				writeError(w, http.StatusBadRequest, "X-Actor must be at most 80 characters without control characters")
				return
			}
			if raw := r.Header.Get("If-Match"); raw != "" {
				value := strings.Trim(raw, "\"")
				revision, err := strconv.ParseUint(value, 10, 64)
				if err != nil || revision == 0 || (raw != value && raw != "\""+value+"\"") {
					writeError(w, http.StatusBadRequest, "If-Match must be a positive task revision, optionally quoted")
					return
				}
				if !strings.HasPrefix(r.URL.Path, "/api/tasks/") {
					writeError(w, http.StatusBadRequest, "If-Match requires an individual task route; use expectedRevision in batch operations")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func mutation(r *http.Request, fallback ...string) store.Mutation {
	actor := strings.TrimSpace(r.Header.Get("X-Actor"))
	if actor == "" {
		actor = "api"
		if len(fallback) > 0 && fallback[0] != "" {
			actor = fallback[0]
		}
	}
	option := store.Mutation{Actor: actor, TaskID: r.PathValue("id")}
	if raw := r.Header.Get("If-Match"); raw != "" {
		revision, _ := strconv.ParseUint(strings.Trim(raw, "\""), 10, 64) // validated by middleware
		option.ExpectedRevision = &revision
	}
	return option
}

func (s *Server) listActivity(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = value
	}
	events := s.store.Activity(r.URL.Query().Get("taskId"))
	more := len(events) > limit
	if more {
		events = events[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"activity": events, "more": more, "retention": store.MaxActivity})
}

func (s *Server) listHistory(w http.ResponseWriter, r *http.Request) {
	s.writeHistory(w, r, r.URL.Query().Get("taskId"))
}

func (s *Server) listTaskHistory(w http.ResponseWriter, r *http.Request) {
	s.writeHistory(w, r, r.PathValue("id"))
}

func (s *Server) writeHistory(w http.ResponseWriter, r *http.Request, taskID string) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = value
	}
	writeJSON(w, http.StatusOK, s.store.History(store.HistoryFilter{TaskID: taskID, Limit: limit, Before: r.URL.Query().Get("before")}))
}

func (s *Server) addWorkLog(w http.ResponseWriter, r *http.Request) {
	var input store.WorkLogInput
	if !decodeRequest(w, r, &input) {
		return
	}
	input.TaskID = r.PathValue("id")
	entry, replayed, err := s.store.AddWorkLogResult(input, mutation(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !replayed {
		s.broadcast()
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (s *Server) previewReport(w http.ResponseWriter, r *http.Request) {
	var request store.ReportRequest
	if !decodeRequest(w, r, &request) {
		return
	}
	preview, err := s.store.PreviewReport(request)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}
