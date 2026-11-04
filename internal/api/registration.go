package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

func emailCodeResponse() map[string]any {
	return map[string]any{"accepted": true, "verification_method": "code",
		"expires_in": int(store.EmailCodeTTL.Seconds()), "retry_after": int(store.EmailCodeRetry.Seconds())}
}

func (a *Server) limitEmailCode(r *http.Request, login string) error {
	if err := a.limit(r, "email-code", login, 3, time.Hour); err != nil {
		return err
	}
	return a.limit(r, "email-code-cooldown", login, 1, store.EmailCodeRetry)
}

func (a *Server) requestRegistrationCode(w http.ResponseWriter, r *http.Request) error {
	if !a.Config.Paid() {
		return failure{404, "registration_disabled"}
	}
	var in struct {
		Login string `json:"login"`
	}
	if err := body(w, r, &in); err != nil {
		return err
	}
	login, err := NormalizeLogin(in.Login, true)
	if err != nil {
		return err
	}
	if err = a.limitEmailCode(r, login); err != nil {
		return err
	}
	if err = a.Store.RequestRegistrationCode(r.Context(), login, a.Config); err != nil {
		return err
	}
	writeJSON(w, 202, emailCodeResponse())
	return nil
}

func (a *Server) verifyRegistrationCode(w http.ResponseWriter, r *http.Request) error {
	if !a.Config.Paid() {
		return failure{404, "registration_disabled"}
	}
	var in struct {
		Login string `json:"login"`
		Code  string `json:"code"`
	}
	if err := body(w, r, &in); err != nil {
		return err
	}
	login, err := NormalizeLogin(in.Login, true)
	if err != nil {
		return err
	}
	if err = a.limit(r, "verify-email-code", login, 30, time.Hour); err != nil {
		return err
	}
	proof, err := a.Store.VerifyRegistrationCode(r.Context(), login, in.Code, a.Config)
	if errors.Is(err, store.ErrDenied) {
		return bad("invalid_verification_code")
	}
	if err != nil {
		return err
	}
	writeJSON(w, 200, proof)
	return nil
}
