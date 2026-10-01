package httpapi

import (
	"fmt"
	"github.com/kcompton15/Todo-Board/internal/store"
	"net/http"
)

func (s *Server) addLink(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if err := store.ValidateNoNullFields(body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var input store.LinkInput
	if err := decodeBytes(body, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	link, revision, err := s.store.LinkTask(r.PathValue("id"), input, mutation(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.broadcast()
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", revision))
	writeJSON(w, http.StatusOK, link)
}

func (s *Server) deleteLink(w http.ResponseWriter, r *http.Request) {
	revision, err := s.store.UnlinkTask(r.PathValue("id"), r.PathValue("linkID"), mutation(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.broadcast()
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", revision))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": r.PathValue("linkID"), "revision": revision})
}
