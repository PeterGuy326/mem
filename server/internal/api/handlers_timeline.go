package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/PeterGuy326/mem/server/internal/auth"
)

// handleTimeline → GET /v1/timeline?year=YYYY[&until=YYYY]
// Returns files grouped by month, ordered by COALESCE(timeline_at, created_at).
// SPEC §F6.3.
func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	q := r.URL.Query()

	yearStr := q.Get("year")
	if yearStr == "" {
		writeError(w, http.StatusBadRequest, "missing_year", "?year=YYYY or ?year=YYYY-YYYY required")
		return
	}
	from, to, err := parseYearRange(yearStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_year", err.Error())
		return
	}

	rows, err := s.File.Pool().Query(r.Context(),
		`SELECT id, name, path, mime,
		        COALESCE(timeline_at, created_at) AS at,
		        summary, caption
		   FROM files
		  WHERE user_id = $1
		    AND COALESCE(timeline_at, created_at) >= $2
		    AND COALESCE(timeline_at, created_at) <  $3
		  ORDER BY at ASC`,
		u.ID, from, to,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	defer rows.Close()

	type entry struct {
		ID      string    `json:"id"`
		Name    string    `json:"name"`
		Path    string    `json:"path"`
		MIME    string    `json:"mime"`
		At      time.Time `json:"at"`
		Summary *string   `json:"summary,omitempty"`
		Caption *string   `json:"caption,omitempty"`
	}
	groups := map[string][]entry{}
	order := []string{}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.ID, &e.Name, &e.Path, &e.MIME, &e.At, &e.Summary, &e.Caption); err != nil {
			writeError(w, http.StatusInternalServerError, "scan", err.Error())
			return
		}
		if !tokenAllowsPath(r, e.Path) {
			continue
		}
		key := e.At.Format("2006-01")
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], e)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "rows", err.Error())
		return
	}

	// Stable shape: [{ month, count, files: [...] }] in chronological order.
	type bucket struct {
		Month string  `json:"month"`
		Count int     `json:"count"`
		Files []entry `json:"files"`
	}
	out := make([]bucket, 0, len(order))
	for _, k := range order {
		out = append(out, bucket{Month: k, Count: len(groups[k]), Files: groups[k]})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from":   from,
		"until":  to,
		"months": out,
	})
}

// parseYearRange accepts "2012" → [2012-01-01, 2013-01-01) or
// "2012-2014" → [2012-01-01, 2015-01-01). Times are UTC.
func parseYearRange(s string) (from, to time.Time, err error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "-") {
		parts := strings.SplitN(s, "-", 2)
		y1, e1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		y2, e2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if e1 != nil || e2 != nil || y1 < 1900 || y2 < y1 || y2 > 9999 {
			return time.Time{}, time.Time{}, fmt.Errorf("expected YYYY or YYYY-YYYY, got %q", s)
		}
		return time.Date(y1, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(y2+1, 1, 1, 0, 0, 0, 0, time.UTC), nil
	}
	y, err := strconv.Atoi(s)
	if err != nil || y < 1900 || y > 9999 {
		return time.Time{}, time.Time{}, fmt.Errorf("expected YYYY or YYYY-YYYY, got %q", s)
	}
	return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(y+1, 1, 1, 0, 0, 0, 0, time.UTC), nil
}
