package api

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(c int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = c
	r.ResponseWriter.WriteHeader(c)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(rec, r)
			logger.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

func recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rv := recover(); rv != nil {
					logger.Error("panic",
						"panic", rv,
						"stack", string(debug.Stack()),
					)
					writeErr(w, http.StatusInternalServerError, "internal")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
