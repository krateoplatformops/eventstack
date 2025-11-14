package access

import (
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

// Access logga IP, URL, method, headers, user-agent e latency
func Access(l zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Determina IP client, gestendo X-Forwarded-For se presente
			ip := r.RemoteAddr
			if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
				ip = forwarded
			}

			// Chiama il handler successivo
			next.ServeHTTP(w, r)

			// Log dei response headers
			headers := w.Header()

			l.Info().Str("ip", ip).
				Str("method", r.Method).
				Str("url", r.URL.String()).
				Str("user_agent", r.UserAgent()).
				Dur("latency", time.Since(start)).
				Any("response_headers", headers).
				Msg("http request")
		})
	}
}
