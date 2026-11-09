package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type pageCursor struct {
	Scope string    `json:"scope"`
	Time  time.Time `json:"time"`
	ID    string    `json:"id"`
}

// Only the explicit administrator list routes accept query parameters. Tokens,
// client endpoints and WebSockets keep the existing query prohibition.
func adminListQuery(r *http.Request) bool {
	if r.Method != http.MethodGet || len(r.URL.RawQuery) > 4096 {
		return false
	}
	allowed := map[string]bool{"cursor": true, "limit": true}
	switch r.URL.Path {
	case "/v1/admin/users":
		allowed["q"] = true
	case "/v1/admin/audit":
		allowed["account_id"] = true
	case "/v1/admin/codes", "/v1/admin/redemptions", "/v1/admin/registration-campaigns":
	default:
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) == 6 && strings.Join(parts[:4], "/") == "/v1/admin/registration-campaigns" && validID(parts[4]) == nil && parts[5] == "grants" {
			break
		}
		if len(parts) != 6 || strings.Join(parts[:4], "/") != "/v1/admin/users" || validID(parts[4]) != nil {
			return false
		}
		switch parts[5] {
		case "devices":
			allowed["role"] = true
		case "bindings", "sessions":
		default:
			return false
		}
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return false
	}
	for key, values := range values {
		if !allowed[key] || len(values) != 1 {
			return false
		}
	}
	return true
}

// Cursors are untrusted positions, not authorization. Scope binds them to the
// endpoint, selected account and search; UUID/time values are always parameters.
func (a *Server) listPage(w http.ResponseWriter, r *http.Request, key, scope, query, timeColumn, idColumn string, args ...any) error {
	limit := 50
	if values, exists := r.URL.Query()["limit"]; exists {
		if len(values) != 1 {
			return bad("invalid_page")
		}
		value, err := strconv.Atoi(values[0])
		if err != nil || value < 1 || value > 200 {
			return bad("invalid_page")
		}
		limit = value
	}
	var cursor pageCursor
	var beforeTime, beforeID any
	if values, exists := r.URL.Query()["cursor"]; exists {
		if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > 2048 {
			return bad("invalid_page")
		}
		raw, err := base64.RawURLEncoding.DecodeString(values[0])
		if err != nil {
			return bad("invalid_page")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || cursor.Scope != scope {
			return bad("invalid_page")
		}
		if timeColumn == "" {
			id, err := strconv.ParseInt(cursor.ID, 10, 64)
			if err != nil || id < 1 {
				return bad("invalid_page")
			}
			beforeID = id
		} else {
			if validID(cursor.ID) != nil || cursor.Time.IsZero() || cursor.Time.Year() < 1 || cursor.Time.Year() > 9999 {
				return bad("invalid_page")
			}
			beforeTime, beforeID = cursor.Time, cursor.ID
		}
	}
	n := len(args) + 1
	if timeColumn == "" {
		query += fmt.Sprintf(" AND ($%d::bigint IS NULL OR %s<$%d) ORDER BY %s DESC LIMIT $%d", n, idColumn, n, idColumn, n+1)
		args = append(args, beforeID, limit+1)
	} else {
		query += fmt.Sprintf(" AND ($%d::timestamptz IS NULL OR (%s,%s)<($%d,$%d::uuid)) ORDER BY %s DESC,%s DESC LIMIT $%d",
			n, timeColumn, idColumn, n, n+1, timeColumn, idColumn, n+2)
		args = append(args, beforeTime, beforeID, limit+1)
	}
	rows, err := a.Store.Pool.Query(r.Context(), query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit+1)
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return err
		}
		item := make(map[string]any, len(values))
		for i, field := range rows.FieldDescriptions() {
			item[field.Name] = values[i]
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		cursor = pageCursor{Scope: scope, ID: last["id"].(string)}
		if timeColumn != "" {
			// Callers select the ordering timestamp under its unqualified column name.
			cursor.Time = last[timeColumn[strings.LastIndex(timeColumn, ".")+1:]].(time.Time)
		}
		encoded, err := json.Marshal(cursor)
		if err != nil {
			return err
		}
		next = base64.RawURLEncoding.EncodeToString(encoded)
	}
	writeJSON(w, 200, map[string]any{key: items, "next_cursor": next})
	return nil
}
