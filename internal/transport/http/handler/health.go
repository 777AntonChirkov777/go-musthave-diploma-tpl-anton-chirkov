package handler

import "net/http"

type HealthHandler struct{}

var _ http.Handler = HealthHandler{}

func (HealthHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
}
