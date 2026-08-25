package handler

import "net/http"

func NewRouter(pinger func() error) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health-check", func(w http.ResponseWriter, r *http.Request) {
		if err := pinger(); err != nil {
			WriteError(w, http.StatusServiceUnavailable, err.Error())
		} else {
			WriteJson(w, http.StatusOK, "ok")
		}
	})

	return mux
}
