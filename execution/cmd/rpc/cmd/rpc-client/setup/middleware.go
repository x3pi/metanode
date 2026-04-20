package setup

import (
	"net/http"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// CORSMiddleware adds CORS headers to responses
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE, HEAD")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, Origin")
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// LoggingMiddleware logs incoming requests (optimized for performance)
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only log WebSocket upgrades (skip regular requests for performance)
		if r.Header.Get("Upgrade") == "websocket" {
			logger.Debug("[WS] %s -> Attempting upgrade for %s", r.RemoteAddr, r.URL.RequestURI())
		}

		next.ServeHTTP(w, r)
	})
}
