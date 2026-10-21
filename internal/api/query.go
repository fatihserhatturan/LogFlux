package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fatihserhatturan/logflux/internal/stats"
	"github.com/fatihserhatturan/logflux/internal/storage"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

const (
	defaultLimit = 100
	maxLimit     = 10000
)

var errBadLimit = errors.New("limit must be a positive number")

// EnableStorage registers /api/query, /api/tail and /api/segments
func (s *Server) EnableStorage(store *storage.Store) {
	s.HandleFunc("/api/query", func(w http.ResponseWriter, r *http.Request) {
		if !allowGET(w, r) {
			return
		}
		q, err := parseQuery(r.URL.Query(), time.Now())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		entries, err := store.Query(q)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if entries == nil {
			entries = []*models.LogEntry{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"count":   len(entries),
			"entries": entries,
		})
	})

	s.HandleFunc("/api/tail", func(w http.ResponseWriter, r *http.Request) {
		if !allowGET(w, r) {
			return
		}
		s.handleTail(w, r, store)
	})

	s.HandleFunc("/api/segments", func(w http.ResponseWriter, r *http.Request) {
		if !allowGET(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, store.Segments())
	})
}

// EnableStats registers /api/stats
func (s *Server) EnableStats(st *stats.Stats) {
	s.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		if !allowGET(w, r) {
			return
		}
		v := r.URL.Query()
		now := time.Now()
		since := now.Add(-time.Hour)
		if raw := v.Get("since"); raw != "" {
			t, err := parseTime(raw, now)
			if err != nil {
				writeError(w, http.StatusBadRequest, "since: "+err.Error())
				return
			}
			since = t
		}
		step := time.Minute
		if raw := v.Get("step"); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil || d < time.Minute {
				writeError(w, http.StatusBadRequest, "step must be a duration of at least 1m")
				return
			}
			step = d
		}
		if now.Sub(since)/step > 10000 {
			writeError(w, http.StatusBadRequest, "too many buckets: increase step or shorten range")
			return
		}
		series := st.Range(since, step, splitList(v.Get("source")))
		if series == nil {
			series = []stats.Series{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"since":  since.UTC(),
			"step":   step.String(),
			"series": series,
		})
	})
}

// handleTail streams matching entries as Server-Sent Events
func (s *Server) handleTail(w http.ResponseWriter, r *http.Request, store *storage.Store) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	q, err := parseQuery(r.URL.Query(), time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Live entries are filtered by everything except the time range
	q.Since, q.Until = time.Time{}, time.Time{}
	backlog := 50
	if raw := r.URL.Query().Get("backlog"); raw != "" {
		if backlog, err = strconv.Atoi(raw); err != nil || backlog < 0 || backlog > 1000 {
			writeError(w, http.StatusBadRequest, "backlog must be 0-1000")
			return
		}
	}

	sub := store.Hub().Subscribe(&q, 512)
	defer store.Hub().Unsubscribe(sub)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	send := func(e *models.LogEntry) bool {
		buf.Reset()
		if err := enc.Encode(e); err != nil {
			return true
		}
		// An SSE event ends with a blank line
		_, err := fmt.Fprintf(w, "data: %s\n\n", bytes.TrimRight(buf.Bytes(), "\n"))
		return err == nil
	}
	for _, e := range store.Ring().Last(backlog, &q) {
		if !send(e) {
			return
		}
	}
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-sub.C:
			if !ok || !send(e) {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// parseQuery builds a storage query from URL parameters:
//
//	since, until   RFC 3339, unix seconds, or a duration ago ("15m")
//	source, level  comma-separated lists
//	min_level      minimum level (e.g. warning)
//	q              case-insensitive text in the message
//	field.<name>   exact match on a field (e.g. field.template_id=12)
//	limit          max entries (default 100, max 10000)
func parseQuery(v url.Values, now time.Time) (storage.Query, error) {
	var q storage.Query
	var err error
	if raw := v.Get("since"); raw != "" {
		if q.Since, err = parseTime(raw, now); err != nil {
			return q, fmt.Errorf("since: %w", err)
		}
	}
	if raw := v.Get("until"); raw != "" {
		if q.Until, err = parseTime(raw, now); err != nil {
			return q, fmt.Errorf("until: %w", err)
		}
	}
	q.Sources = splitList(v.Get("source"))
	for _, l := range splitList(v.Get("level")) {
		level, ok := models.LookupLevel(l)
		if !ok {
			return q, fmt.Errorf("unknown level %q", l)
		}
		q.Levels = append(q.Levels, level)
	}
	if raw := v.Get("min_level"); raw != "" {
		level, ok := models.LookupLevel(raw)
		if !ok {
			return q, fmt.Errorf("unknown min_level %q", raw)
		}
		q.MinLevel = level
	}
	q.Text = v.Get("q")
	for key, vals := range v {
		if name := strings.TrimPrefix(key, "field."); name != key && name != "" && len(vals) > 0 {
			if q.Fields == nil {
				q.Fields = make(map[string]string)
			}
			q.Fields[name] = vals[0]
		}
	}
	if q.Limit, err = parseLimit(v.Get("limit")); err != nil {
		return q, err
	}
	return q, nil
}

// parseTime accepts RFC 3339, unix seconds, or a duration meaning "ago"
func parseTime(raw string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return now.Add(-d), nil
	}
	if secs, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(secs, 0), nil
	}
	return time.Time{}, fmt.Errorf("invalid time %q (use RFC 3339, unix seconds or a duration like 15m)", raw)
}

func splitList(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func allowGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	return true
}

// writeError writes a JSON error response
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
