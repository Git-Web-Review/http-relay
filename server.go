package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

// Health is reported as degraded while the Redis subscription is down, so the
// container healthcheck restarts a relay that can no longer receive events.
type healthState struct {
	redisConnected atomic.Bool
}

// NewServer builds the relay's own HTTP surface: a healthcheck for the
// container and a status endpoint to inspect deliveries.
func NewServer(config Config, stats *Stats, health *healthState, startedAt time.Time) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", "GET, HEAD")
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		status := "ok"
		code := http.StatusOK
		if !health.redisConnected.Load() {
			status = "degraded"
			code = http.StatusServiceUnavailable
		}

		writeJSON(writer, code, map[string]any{
			"status":         status,
			"redisConnected": health.redisConnected.Load(),
			"uptimeSeconds":  int64(time.Since(startedAt).Seconds()),
		})
	})

	mux.HandleFunc("/status", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", "GET")
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		writeJSON(writer, http.StatusOK, map[string]any{
			"service":        "http-relay",
			"redisChannel":   config.RedisChannel,
			"redisConnected": health.redisConnected.Load(),
			"dryRun":         config.DryRun,
			"workers":        config.Workers,
			"queueSize":      config.QueueSize,
			"maxAttempts":    config.MaxAttempts,
			"timeoutSeconds": config.Timeout.Seconds(),
			"uptimeSeconds":  int64(time.Since(startedAt).Seconds()),
			"deliveries":     stats.Snapshot(),
		})
	})

	return &http.Server{
		Addr:              config.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func writeJSON(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(body); err != nil {
		log.Printf("cannot write response: %v", err)
	}
}
