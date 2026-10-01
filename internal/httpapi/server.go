package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kcompton15/Todo-Board/internal/parse"
	"github.com/kcompton15/Todo-Board/internal/store"
)

const maxRequestBody = 1 << 20

type Server struct {
	store     *store.Store
	hub       *Hub
	logger    *slog.Logger
	indexHTML []byte
}

func New(taskStore *store.Store, hub *Hub, logger *slog.Logger, indexHTML []byte) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{store: taskStore, hub: hub, logger: logger, indexHTML: indexHTML}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/activity", s.listActivity)
	mux.HandleFunc("GET /api/history", s.listHistory)
	mux.HandleFunc("GET /api/tasks/{id}/history", s.listTaskHistory)
	mux.HandleFunc("POST /api/tasks/{id}/work-log", s.addWorkLog)
	mux.HandleFunc("POST /api/reports/preview", s.previewReport)
	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("POST /api/tasks", s.createTasks)
	mux.HandleFunc("GET /api/tasks/{id}", s.getTask)
	mux.HandleFunc("PUT /api/tasks/{id}", s.replaceTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", s.patchTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.deleteTask)
	mux.HandleFunc("POST /api/tasks/{id}/subtasks", s.addSubtasks)
	mux.HandleFunc("POST /api/tasks/{id}/links", s.addLink)
	mux.HandleFunc("DELETE /api/tasks/{id}/links/{linkID}", s.deleteLink)
	mux.HandleFunc("PATCH /api/tasks/{id}/subtasks/{subtaskID}", s.patchSubtask)
	mux.HandleFunc("DELETE /api/tasks/{id}/subtasks/{subtaskID}", s.deleteSubtask)
	mux.HandleFunc("POST /api/import", s.importTasks)
	mux.HandleFunc("POST /api/ops", s.applyOps)
	mux.HandleFunc("GET /api/projects", s.listProjects)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /{$}", s.index)
	return securityHeaders(mutationHeaders(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

func (s *Server) index(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.indexHTML)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	filter := store.Filter{
		Lane: r.URL.Query().Get("lane"), Project: r.URL.Query().Get("project"),
		Tag: r.URL.Query().Get("tag"), Kind: r.URL.Query().Get("kind"), Query: r.URL.Query().Get("q"),
	}
	if filter.Lane != "" && !store.IsLane(filter.Lane) {
		writeError(w, http.StatusBadRequest, "invalid lane")
		return
	}
	if filter.Kind != "" && !store.IsKind(filter.Kind) {
		writeError(w, http.StatusBadRequest, "invalid kind")
		return
	}
	if value := r.URL.Query().Get("priority"); value != "" {
		priority, err := strconv.Atoi(value)
		if err != nil || priority < 1 || priority > 3 {
			writeError(w, http.StatusBadRequest, "priority must be 1, 2, or 3")
			return
		}
		filter.Priority = priority
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": s.store.List(filter)})
}

func (s *Server) createTasks(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if err := store.ValidateNoNullFields(body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var envelope struct {
		Tasks []store.TaskInput `json:"tasks"`
	}
	var inputs []store.TaskInput
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if _, exists := object["tasks"]; exists {
		if err := decodeBytes(body, &envelope); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		inputs = envelope.Tasks
	} else {
		var input store.TaskInput
		if err := decodeBytes(body, &input); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		inputs = []store.TaskInput{input}
	}
	tasks, err := s.store.CreateMany(inputs, "", mutation(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.broadcast()
	writeJSON(w, http.StatusCreated, map[string]any{"tasks": tasks})
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", task.Revision))
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) replaceTask(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if err := store.ValidateNoNullFields(body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var input store.TaskInput
	if err := decodeBytes(body, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	task, err := s.store.Replace(r.PathValue("id"), input, mutation(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.broadcast()
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", task.Revision))
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) patchTask(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if err := store.ValidateNoNullFields(body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var patch store.TaskPatch
	if err := decodeBytes(body, &patch); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	task, err := s.store.Patch(r.PathValue("id"), patch, mutation(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.broadcast()
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", task.Revision))
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(r.PathValue("id"), mutation(r)); err != nil {
		writeStoreError(w, err)
		return
	}
	s.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addSubtasks(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title  string   `json:"title"`
		Titles []string `json:"titles"`
		Lane   string   `json:"lane"`
	}
	if !decodeRequest(w, r, &input) {
		return
	}
	inputs := make(store.SubtaskInputs, 0, len(input.Titles))
	if input.Title != "" {
		inputs = append(inputs, store.SubtaskInput{Title: input.Title, Lane: input.Lane})
	} else {
		for _, title := range input.Titles {
			inputs = append(inputs, store.SubtaskInput{Title: title, Lane: input.Lane})
		}
	}
	created, revision, err := s.store.AddSubtasks(r.PathValue("id"), inputs, mutation(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.broadcast()
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", revision))
	if len(created) == 1 && input.Title != "" {
		writeJSON(w, http.StatusCreated, created[0])
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"subtasks": created})
}

func (s *Server) patchSubtask(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if err := store.ValidateNoNullFields(body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var patch store.SubtaskPatch
	if err := decodeBytes(body, &patch); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	subtask, revision, err := s.store.PatchSubtask(r.PathValue("id"), r.PathValue("subtaskID"), patch, mutation(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.broadcast()
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", revision))
	writeJSON(w, http.StatusOK, subtask)
}

func (s *Server) deleteSubtask(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteSubtask(r.PathValue("id"), r.PathValue("subtaskID"), mutation(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) importTasks(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var inputs []store.TaskInput
	var err error
	switch mediaType {
	case "text/plain", "":
		inputs, err = parse.Tasks(string(body))
	case "application/json":
		if nullErr := store.ValidateNoNullFields(body); nullErr != nil {
			err = nullErr
		} else {
			err = decodeBytes(body, &inputs)
		}
	default:
		writeError(w, http.StatusUnsupportedMediaType, "content type must be text/plain or application/json")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	source := strings.TrimSpace(r.Header.Get("X-Source"))
	if source == "" {
		source = "cli"
	}
	tasks, err := s.store.CreateMany(inputs, source, mutation(r, source))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.broadcast()
	writeJSON(w, http.StatusCreated, map[string]any{"created": len(tasks), "tasks": tasks})
}

func (s *Server) applyOps(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if err := store.ValidateNoNullFields(body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var envelope store.OpsEnvelope
	if err := decodeBytes(body, &envelope); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if envelope.Source == "" {
		envelope.Source = "cli"
	}
	tasks, replayed, err := s.store.ApplyEnvelope(envelope, mutation(r, envelope.Source))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeStoreError(w, err)
		return
	}
	applied := len(envelope.Ops)
	if replayed {
		applied = 0
	} else {
		s.broadcast()
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": applied, "tasks": tasks, "alreadyApplied": replayed})
}

func (s *Server) listProjects(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"projects": s.store.Projects()})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	channel, unsubscribe := s.hub.Subscribe()
	defer unsubscribe()
	initial, err := json.Marshal(map[string]any{"tasks": s.store.List(store.Filter{})})
	if err != nil {
		return
	}
	if !writeEvent(w, initial) {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case payload, open := <-channel:
			if !open || !writeEvent(w, payload) {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeEvent(w io.Writer, payload []byte) bool {
	_, err := fmt.Fprintf(w, "event: tasks\ndata: %s\n\n", payload)
	return err == nil
}

func (s *Server) broadcast() {
	payload, err := json.Marshal(map[string]any{"tasks": s.store.List(store.Filter{})})
	if err != nil {
		s.logger.Error("marshal SSE state", "error", err)
		return
	}
	s.hub.Publish(payload)
}

func (s *Server) Broadcast() {
	s.broadcast()
}

func decodeRequest(w http.ResponseWriter, r *http.Request, destination any) bool {
	body, ok := readBody(w, r)
	if !ok {
		return false
	}
	if err := decodeBytes(body, destination); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read request body: "+err.Error())
		return nil, false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		writeError(w, http.StatusBadRequest, "request body is required")
		return nil, false
	}
	return body, true
}

func decodeBytes(data []byte, destination any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("invalid JSON: multiple values are not allowed")
		}
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrHistoryConflict) || errors.Is(err, store.ErrActionConflict) {
		code := "history_conflict"
		if errors.Is(err, store.ErrActionConflict) {
			code = "action_conflict"
		}
		writeJSON(w, http.StatusConflict, map[string]string{"code": code, "error": err.Error()})
		return
	}
	if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrOpenSubtasks) {
		code := "revision_conflict"
		if errors.Is(err, store.ErrOpenSubtasks) {
			code = "open_subtasks"
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": code})
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, store.ErrPersistence) {
		writeError(w, http.StatusInternalServerError, "could not persist task state")
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
