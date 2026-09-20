package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	application "diplom/internal/application/user"
	domain "diplom/internal/domain/user"
	"diplom/internal/transport/http/handler"
)

const SessionCookieName = handler.SessionCookieName

type SessionAuthenticator interface {
	Authenticate(context.Context, string) (domain.ID, error)
}

type userIDKey struct{}

// UserID returns the identity verified by RequireAuth.
func UserID(ctx context.Context) (domain.ID, bool) {
	id, ok := ctx.Value(userIDKey{}).(domain.ID)
	return id, ok
}

// RequireAuth authenticates a bearer token or the session cookie for protected routes.
func RequireAuth(users SessionAuthenticator, logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		token, err := requestToken(r)
		if err == nil {
			var id domain.ID
			id, err = users.Authenticate(r.Context(), token)
			if err == nil {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey{}, id)))
				return
			}
		}
		status := http.StatusUnauthorized
		if !errors.Is(err, application.ErrUnauthenticated) {
			status = http.StatusInternalServerError
			logger.ErrorContext(r.Context(), "session authentication failed", "error", err)
		}
		http.Error(w, http.StatusText(status), status)
	})
}

func requestToken(r *http.Request) (string, error) {
	if values, present := r.Header["Authorization"]; present {
		if len(values) != 1 {
			return "", application.ErrUnauthenticated
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return "", application.ErrUnauthenticated
		}
		return parts[1], nil
	}
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return "", application.ErrUnauthenticated
	}
	return cookie.Value, nil
}
