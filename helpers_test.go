package main

import (
	"context"
	"testing"
	"time"
)

func testConfig(frontendURL string) Config {
	return Config{
		FrontendURL:    frontendURL,
		Workers:        2,
		QueueSize:      16,
		Timeout:        2 * time.Second,
		MaxAttempts:    3,
		RetryBaseDelay: time.Millisecond,
		RetryMaxDelay:  5 * time.Millisecond,
		UserAgent:      "test-agent",
	}
}

func newTestDispatcher(t *testing.T, config Config) (*Dispatcher, func()) {
	t.Helper()

	dispatcher := NewDispatcher(config)
	dispatcher.Start(context.Background())
	return dispatcher, dispatcher.Close
}

func webhookEvent(url string, enabled bool) *Event {
	return &Event{
		Type:           "REVIEW_PENDING",
		NotificationID: "notification-1",
		CreatedAt:      "2026-01-02T03:04:05.000Z",
		User: EventUser{
			ID:                          "user-1",
			Email:                       "dev@example.test",
			Nickname:                    "dev",
			Locale:                      "EN",
			WebhookNotificationsEnabled: enabled,
			WebhookURL:                  url,
		},
		Payload: map[string]any{
			"reviewId":   "review-42",
			"title":      "Fix the parser",
			"ownerEmail": "owner@example.test",
		},
	}
}
