package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// An unknown notification type must still be forwarded: the relay does not
// interpret the type, so a new one needs no relay release.
func TestUnknownEventTypeIsStillForwardedVerbatim(t *testing.T) {
	received := make(chan WebhookPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload WebhookPayload
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode body: %v", err)
		}
		received <- payload
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	event := webhookEvent(server.URL, true)
	event.Type = "SOMETHING_NEW"
	event.Payload["brandNewField"] = []any{"a", "b"}

	dispatcher, closeDispatcher := newTestDispatcher(t, testConfig("https://app.test"))
	dispatcher.Enqueue(event)
	closeDispatcher()

	payload := <-received
	if payload.Event != "SOMETHING_NEW" {
		t.Errorf("event = %q, want SOMETHING_NEW", payload.Event)
	}

	forwarded, ok := payload.Payload["brandNewField"].([]any)
	if !ok || len(forwarded) != 2 {
		t.Errorf("unknown payload field was not forwarded: %v", payload.Payload["brandNewField"])
	}
}

func TestDispatcherPostsTheEventAsJSON(t *testing.T) {
	received := make(chan WebhookPayload, 1)
	headers := make(chan http.Header, 1)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload WebhookPayload
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode body: %v", err)
		}

		headers <- request.Header.Clone()
		received <- payload
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	dispatcher, closeDispatcher := newTestDispatcher(t, testConfig("https://app.test"))
	dispatcher.Enqueue(webhookEvent(server.URL, true))
	closeDispatcher()

	select {
	case payload := <-received:
		if payload.Event != "REVIEW_PENDING" {
			t.Errorf("event = %q, want REVIEW_PENDING", payload.Event)
		}
		if payload.URL != "https://app.test/review/review-42" {
			t.Errorf("review url = %q", payload.URL)
		}
		if payload.NotificationID != "notification-1" {
			t.Errorf("notificationId = %q", payload.NotificationID)
		}
		if payload.CreatedAt != "2026-01-02T03:04:05.000Z" {
			t.Errorf("createdAt = %q", payload.CreatedAt)
		}
		if payload.SentAt == "" || payload.DeliveryID == "" {
			t.Errorf("delivery metadata is missing: sentAt=%q deliveryId=%q", payload.SentAt, payload.DeliveryID)
		}
		if payload.User.Email != "dev@example.test" || payload.User.Locale != "EN" {
			t.Errorf("user = %+v", payload.User)
		}
		// The whole payload is forwarded untouched, key for key.
		if payload.Payload["title"] != "Fix the parser" ||
			payload.Payload["reviewId"] != "review-42" ||
			payload.Payload["ownerEmail"] != "owner@example.test" {
			t.Errorf("raw payload was not forwarded verbatim: %v", payload.Payload)
		}
	default:
		t.Fatal("no webhook was delivered")
	}

	header := <-headers
	if header.Get("X-Git-Web-Review-Event") != "REVIEW_PENDING" {
		t.Errorf("event header = %q", header.Get("X-Git-Web-Review-Event"))
	}
	if header.Get("X-Git-Web-Review-Delivery") == "" {
		t.Error("delivery header is missing")
	}
	if header.Get("User-Agent") != "test-agent" {
		t.Errorf("user agent = %q", header.Get("User-Agent"))
	}

	if delivered := dispatcher.Stats().Delivered.Load(); delivered != 1 {
		t.Errorf("delivered = %d, want 1", delivered)
	}
}

func TestDispatcherSkipsDisabledAndInvalidTargets(t *testing.T) {
	dispatcher, closeDispatcher := newTestDispatcher(t, testConfig("https://app.test"))
	dispatcher.Enqueue(webhookEvent("https://example.test/hook", false))
	dispatcher.Enqueue(webhookEvent("", true))
	dispatcher.Enqueue(webhookEvent("ftp://example.test/hook", true))
	closeDispatcher()

	stats := dispatcher.Stats()
	if skipped := stats.Skipped.Load(); skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	if invalid := stats.InvalidTarget.Load(); invalid != 1 {
		t.Errorf("invalidTarget = %d, want 1", invalid)
	}
	if queued := stats.Queued.Load(); queued != 0 {
		t.Errorf("queued = %d, want 0", queued)
	}
}

func TestDispatcherRetriesServerErrorsThenSucceeds(t *testing.T) {
	var attempts atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}

		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dispatcher, closeDispatcher := newTestDispatcher(t, testConfig("https://app.test"))
	dispatcher.Enqueue(webhookEvent(server.URL, true))
	closeDispatcher()

	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
	if delivered := dispatcher.Stats().Delivered.Load(); delivered != 1 {
		t.Errorf("delivered = %d, want 1", delivered)
	}
	if retried := dispatcher.Stats().Retried.Load(); retried != 2 {
		t.Errorf("retried = %d, want 2", retried)
	}
}

func TestDispatcherGivesUpAfterMaxAttempts(t *testing.T) {
	var attempts atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	dispatcher, closeDispatcher := newTestDispatcher(t, testConfig("https://app.test"))
	dispatcher.Enqueue(webhookEvent(server.URL, true))
	closeDispatcher()

	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
	if failed := dispatcher.Stats().Failed.Load(); failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	if retried := dispatcher.Stats().Retried.Load(); retried != 2 {
		t.Errorf("retried = %d, want 2", retried)
	}
}

func TestDispatcherRetriesTimeoutsAndUnreachableHosts(t *testing.T) {
	var slowAttempts atomic.Int64
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		slowAttempts.Add(1)
		<-release
	}))
	defer slow.Close()
	defer close(release)

	unreachable := httptest.NewServer(http.NotFoundHandler())
	unreachableURL := unreachable.URL
	unreachable.Close()

	config := testConfig("https://app.test")
	config.Timeout = 50 * time.Millisecond
	dispatcher, closeDispatcher := newTestDispatcher(t, config)
	dispatcher.Enqueue(webhookEvent(slow.URL, true))
	dispatcher.Enqueue(webhookEvent(unreachableURL, true))
	closeDispatcher()

	if got := slowAttempts.Load(); got != 3 {
		t.Errorf("attempts on a timing out endpoint = %d, want 3", got)
	}
	if failed := dispatcher.Stats().Failed.Load(); failed != 2 {
		t.Errorf("failed = %d, want 2", failed)
	}
	if retried := dispatcher.Stats().Retried.Load(); retried != 4 {
		t.Errorf("retried = %d, want 4", retried)
	}
}

func TestDispatcherDoesNotRetryClientErrors(t *testing.T) {
	var attempts atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	dispatcher, closeDispatcher := newTestDispatcher(t, testConfig("https://app.test"))
	dispatcher.Enqueue(webhookEvent(server.URL, true))
	closeDispatcher()

	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	if failed := dispatcher.Stats().Failed.Load(); failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
}

func TestDispatcherDryRunDoesNotCallTheEndpoint(t *testing.T) {
	var attempts atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		attempts.Add(1)
	}))
	defer server.Close()

	config := testConfig("https://app.test")
	config.DryRun = true
	dispatcher, closeDispatcher := newTestDispatcher(t, config)
	dispatcher.Enqueue(webhookEvent(server.URL, true))
	closeDispatcher()

	if got := attempts.Load(); got != 0 {
		t.Fatalf("dry-run reached the endpoint %d time(s)", got)
	}
}

func TestDispatcherBlocksPrivateAddressesWhenConfigured(t *testing.T) {
	var attempts atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		attempts.Add(1)
	}))
	defer server.Close()

	config := testConfig("https://app.test")
	config.BlockPrivateNetworks = true
	config.MaxAttempts = 1
	dispatcher, closeDispatcher := newTestDispatcher(t, config)
	dispatcher.Enqueue(webhookEvent(server.URL, true))
	closeDispatcher()

	if got := attempts.Load(); got != 0 {
		t.Fatalf("loopback endpoint was reached %d time(s)", got)
	}
	if failed := dispatcher.Stats().Failed.Load(); failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
}

func TestDispatcherReachesLoopbackOnlyWhenBlockingIsOff(t *testing.T) {
	var attempts atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := testConfig("https://app.test")
	config.BlockPrivateNetworks = false
	dispatcher, closeDispatcher := newTestDispatcher(t, config)
	dispatcher.Enqueue(webhookEvent(server.URL, true))
	closeDispatcher()

	if got := attempts.Load(); got != 1 {
		t.Fatalf("loopback endpoint was reached %d time(s), want 1", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("12"); got != 12*time.Second {
		t.Errorf("parseRetryAfter(\"12\") = %s, want 12s", got)
	}
	if got := parseRetryAfter("not-a-delay"); got != 0 {
		t.Errorf("parseRetryAfter(garbage) = %s, want 0", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("parseRetryAfter(\"\") = %s, want 0", got)
	}
}
