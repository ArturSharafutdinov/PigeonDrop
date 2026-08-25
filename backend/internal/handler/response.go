package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type errorResponse struct {
	Error string `json:"error"`
}

func WriteJson(w http.ResponseWriter, status int, message any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(message); err != nil {
		slog.Error("encoding response", "err", err)
	}
}

func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJson(w, status, errorResponse{Error: msg})
}
