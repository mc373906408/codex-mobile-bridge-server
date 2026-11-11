package api

import (
	"encoding/base64"
	"net/http"
)

const officialSponsorshipOrigin = "https://bridge.waken.cloudns.be:8787"

// The purchase image belongs to the service. Updating it does not require a desktop release.
func (a *Server) sponsorship(w http.ResponseWriter, r *http.Request) error {
	if !a.Config.Paid() || a.Config.Origin != officialSponsorshipOrigin {
		return failure{404, "sponsorship_unavailable"}
	}
	image, err := webFiles.ReadFile("web/sponsorship.png")
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"image": "data:image/png;base64," + base64.StdEncoding.EncodeToString(image)})
	return nil
}
