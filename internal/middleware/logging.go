package middleware

import (
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"
)

// Options controls access log behavior.
type Options struct {
	Enabled bool // if false, middleware is a no-op
	Sample  int  // log 1 out of N requests (>=1). 1 = log all
}

// AccessLogger returns a Chi middleware that logs one line per request
// with method, path, status, duration, remote, and req_id (if present).
func AccessLogger(opts Options) func(http.Handler) http.Handler {
	if !opts.Enabled {
		return func(next http.Handler) http.Handler { return next }
	}
	if opts.Sample < 1 {
		opts.Sample = 1
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// simple sampling
			if opts.Sample > 1 && rand.Intn(opts.Sample) != 0 {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			sr := &statusRecorder{ResponseWriter: w, code: 200}
			next.ServeHTTP(sr, r)

			// Chi's RequestID middleware stores the ID in context
			reqID := chimw.GetReqID(r.Context())
			remote := r.RemoteAddr // RealIP middleware helps make this accurate

			log.Info().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", sr.code).
				Dur("duration", time.Since(start)).
				Str("remote", remote).
				Str("req_id", reqID).
				Msg("http_request")
		})
	}
}

// AccessLoggerFromEnv reads env and builds an AccessLogger:
//
//	ACCESS_LOG=true|false (default false)
//	ACCESS_LOG_SAMPLE=N  (default 1 = log all when enabled)
func AccessLoggerFromEnv() func(http.Handler) http.Handler {
	// default: disabled locally unless you explicitly turn it on
	enabled := false
	if v := os.Getenv("ACCESS_LOG"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			enabled = b
		}
	}

	sample := 1
	if v := os.Getenv("ACCESS_LOG_SAMPLE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			sample = n
		}
	}
	return AccessLogger(Options{Enabled: enabled, Sample: sample})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.code = code
	sr.ResponseWriter.WriteHeader(code)
}
