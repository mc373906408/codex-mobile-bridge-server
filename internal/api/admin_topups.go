package api

import (
	"net/http"
	"strings"
	"unicode/utf8"
)

func (a *Server) adminTopup(w http.ResponseWriter, r *http.Request) error {
	p, err := a.administrator(r)
	if err != nil {
		return err
	}
	if !a.Config.Paid() {
		return failure{404, "topup_disabled"}
	}
	id := r.PathValue("id")
	if err = validID(id); err != nil {
		return err
	}
	var in struct {
		Days      int    `json:"days"`
		Note      string `json:"note"`
		RequestID string `json:"request_id"`
	}
	if err = body(w, r, &in); err != nil {
		return err
	}
	in.Note = strings.TrimSpace(in.Note)
	if in.Days < 1 || in.Days > 3650 || len(in.Note) > 1000 || !utf8.ValidString(in.Note) || strings.ContainsRune(in.Note, '\x00') {
		return bad("invalid_topup")
	}
	if err = validID(in.RequestID); err != nil {
		return err
	}
	result, err := a.Store.Topup(r.Context(), p, id, in.RequestID, in.Days, in.Note)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}
