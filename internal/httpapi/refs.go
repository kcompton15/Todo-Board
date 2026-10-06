package httpapi

import (
	"errors"
	"net/http"

	"github.com/kcompton15/Todo-Board/internal/store"
)

// withRefs rewrites card and step numbers in the path to internal IDs before the
// handler runs, so revision checks and history keep using IDs.
func (s *Server) withRefs(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ref := r.PathValue("id")
		task, step, err := s.store.Resolve(ref)
		if errors.Is(err, store.ErrNotFound) {
			next(w, r)
			return
		}
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if step != nil {
			writeError(w, http.StatusBadRequest, "step reference "+ref+" needs a subtask route such as /api/tasks/N/subtasks/M")
			return
		}
		r.SetPathValue("id", task.ID)
		if subRef := r.PathValue("subtaskID"); subRef != "" {
			if sub, ok := store.FindSubtask(task, subRef); ok {
				r.SetPathValue("subtaskID", sub.ID)
			}
		}
		next(w, r)
	}
}

func (s *Server) resolveTaskID(ref string) string {
	if ref == "" {
		return ref
	}
	task, step, err := s.store.Resolve(ref)
	if err != nil || step != nil {
		return ref
	}
	return task.ID
}
