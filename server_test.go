package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthEndpointReportsRedisState(t *testing.T) {
	health := &healthState{}
	stats := &Stats{}
	server := NewServer(testConfig("https://app.test"), stats, health, time.Now())

	recorder := httptest.NewRecorder()
	server.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("health without redis = %d, want 503", recorder.Code)
	}

	health.redisConnected.Store(true)
	recorder = httptest.NewRecorder()
	server.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusOK {
		t.Errorf("health with redis = %d, want 200", recorder.Code)
	}

	stats.Delivered.Add(3)
	recorder = httptest.NewRecorder()
	server.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	var body struct {
		Service    string           `json:"service"`
		Deliveries map[string]int64 `json:"deliveries"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body.Service != "http-relay" {
		t.Errorf("service = %q", body.Service)
	}
	if body.Deliveries["delivered"] != 3 {
		t.Errorf("delivered = %d, want 3", body.Deliveries["delivered"])
	}
}
