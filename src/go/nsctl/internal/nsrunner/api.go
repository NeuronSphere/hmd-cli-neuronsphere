package nsrunner

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// The routes below are Argo's, because hmd-ms-deployment already speaks them:
// submit_workfow POSTs to /api/v1/workflows/<namespace> and reads
// metadata.name out of the reply. Keeping the shape means adding a local
// runner is a client selection rather than a second protocol -- which is what
// SPEC014 relies on to call this change small.
//
// What travels inside the envelope is not Argo's. `workflow` carries the node
// manifest generate_local_deployment already produces; there is no argoproj.io
// CRD locally, and inventing one that nothing consumes would be ceremony.

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var sub Submission
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("reading the submission: %w", err))
		return
	}
	if sub.Namespace == "" {
		sub.Namespace = r.PathValue("namespace")
	}

	wf, err := s.Submit(sub)
	if err != nil {
		var busy *BusyError
		if errors.As(err, &busy) {
			// 409 rather than an error: the caller asked for something
			// reasonable and the answer is "not yet", which is a different
			// thing from a malformed request.
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"metadata": map[string]any{"name": wf.Name, "namespace": wf.Namespace},
	})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.Store.Get(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no workflow named %s", r.PathValue("name")))
		return
	}
	snap := rec.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"metadata": map[string]any{"name": snap.Name, "namespace": snap.Namespace},
		"status":   snap,
	})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	items := s.Store.List()
	if env := r.URL.Query().Get("environment"); env != "" {
		filtered := items[:0]
		for _, it := range items {
			if it.Environment == env {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleLog streams a workflow's output.
//
// `follow` is what makes attaching to someone else's deployment work: the
// backlog is replayed first, then new lines as they arrive, so a caller that
// joins halfway still sees the whole run rather than the tail of it.
func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.Store.Get(name); !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("no workflow named %s", name))
		return
	}
	log := s.Store.Log(name)
	if log == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("no log for %s", name))
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	backlog, ch := log.Subscribe()
	defer log.Unsubscribe(ch)
	for _, line := range backlog {
		_, _ = w.Write([]byte(line))
	}
	if flusher != nil {
		flusher.Flush()
	}
	if ch == nil || r.URL.Query().Get("follow") != "true" {
		return
	}

	for {
		select {
		case line, open := <-ch:
			if !open {
				return
			}
			if _, err := w.Write([]byte(line)); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		case <-r.Context().Done():
			// The caller hung up. The deployment carries on regardless -- that
			// it no longer depends on anyone watching is the whole point.
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{"message": err.Error()})
}
