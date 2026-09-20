package handler

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	application "diplom/internal/application/user"
	domain "diplom/internal/domain/user"
)

const (
	SessionCookieName  = "session"
	maxCredentialsBody = 64 << 10
)

func decodeCredentials(w http.ResponseWriter, r *http.Request) (string, string, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return "", "", domain.ErrInvalidCredentials
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCredentialsBody)
	defer r.Body.Close()
	var body struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return "", "", err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", "", domain.ErrInvalidCredentials
	}
	if err := domain.ValidateCredentials(body.Login, body.Password); err != nil {
		return "", "", err
	}
	return body.Login, body.Password, nil
}

func writeAuthentication(w http.ResponseWriter, r *http.Request, result application.Authenticated) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    result.Token,
		Path:     "/",
		Expires:  result.ExpiresAt,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Authorization", "Bearer "+result.Token)
	w.WriteHeader(http.StatusOK)
}
