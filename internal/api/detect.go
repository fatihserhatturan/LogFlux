package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fatihserhatturan/logflux/internal/alert"
	"github.com/fatihserhatturan/logflux/internal/detect"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

// EnableAlerts registers /api/alerts
//
//	since  RFC 3339, unix seconds or a duration ago ("1h")
//	kind   new_pattern, spike, silence or rule
//	limit  max alerts (default 100)
func (s *Server) EnableAlerts(mgr *alert.Manager) {
	s.HandleFunc("/api/alerts", func(w http.ResponseWriter, r *http.Request) {
		if !allowGET(w, r) {
			return
		}
		v := r.URL.Query()
		var q alert.Query
		if raw := v.Get("since"); raw != "" {
			t, err := parseTime(raw, time.Now())
			if err != nil {
				writeError(w, http.StatusBadRequest, "since: "+err.Error())
				return
			}
			q.Since = t
		}
		q.Kind = v.Get("kind")
		limit, err := parseLimit(v.Get("limit"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		q.Limit = limit
		alerts := mgr.List(q)
		writeJSON(w, http.StatusOK, map[string]interface{}{"count": len(alerts), "alerts": alerts})
	})
}

// templateView is a cluster as returned by /api/templates
type templateView struct {
	ID        int              `json:"id"`
	Template  string           `json:"template"`
	Count     int64            `json:"count"`
	FirstSeen time.Time        `json:"first_seen"`
	LastSeen  time.Time        `json:"last_seen"`
	MaxLevel  models.LogLevel  `json:"max_level"`
	Sources   map[string]int64 `json:"sources"`
	Sample    string           `json:"sample"`
}

// EnableTemplates registers /api/templates
//
//	sort       count (default), recent (last seen) or new (first seen)
//	min_level  only templates that reached this level
//	source     only templates seen from this source
//	q          case-insensitive text in the template
//	limit      max templates (default 100)
func (s *Server) EnableTemplates(d *detect.Drain) {
	s.HandleFunc("/api/templates", func(w http.ResponseWriter, r *http.Request) {
		if !allowGET(w, r) {
			return
		}
		v := r.URL.Query()
		limit, err := parseLimit(v.Get("limit"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var minLevel models.LogLevel
		if raw := v.Get("min_level"); raw != "" {
			l, ok := models.LookupLevel(raw)
			if !ok {
				writeError(w, http.StatusBadRequest, "unknown min_level")
				return
			}
			minLevel = l
		}
		source := v.Get("source")
		text := strings.ToLower(v.Get("q"))

		var out []templateView
		for _, c := range d.Clusters() {
			if minLevel != "" && c.MaxLevel.Severity() < minLevel.Severity() {
				continue
			}
			if source != "" && c.Sources[source] == 0 {
				continue
			}
			tmpl := c.Template()
			if text != "" && !strings.Contains(strings.ToLower(tmpl), text) {
				continue
			}
			out = append(out, templateView{
				ID: c.ID, Template: tmpl, Count: c.Size, FirstSeen: c.FirstSeen, LastSeen: c.LastSeen,
				MaxLevel: c.MaxLevel, Sources: c.Sources, Sample: c.Sample,
			})
		}

		switch v.Get("sort") {
		case "", "count":
			sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
		case "recent":
			sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
		case "new":
			sort.Slice(out, func(i, j int) bool { return out[i].FirstSeen.After(out[j].FirstSeen) })
		default:
			writeError(w, http.StatusBadRequest, "sort must be count, recent or new")
			return
		}
		total := len(out)
		if len(out) > limit {
			out = out[:limit]
		}
		if out == nil {
			out = []templateView{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"total": total, "templates": out})
	})
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, errBadLimit
	}
	if n > maxLimit {
		n = maxLimit
	}
	return n, nil
}
