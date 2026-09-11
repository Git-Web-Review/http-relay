package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

func TestParseEventRejectsEventWithoutUser(t *testing.T) {
	if _, err := ParseEvent([]byte(`{"type":"TEXT"}`)); err == nil {
		t.Fatal("expected an event without a user to be rejected")
	}

	if _, err := ParseEvent([]byte(`not json`)); err == nil {
		t.Fatal("expected invalid JSON to be rejected")
	}
}

func TestReviewURLPrefersTheReviewIdAndFallsBackToGitweb(t *testing.T) {
	event := webhookEvent("https://example.test/hook", true)
	if got := event.ReviewURL("https://app.test/"); got != "https://app.test/review/review-42" {
		t.Errorf("ReviewURL = %q", got)
	}

	delete(event.Payload, "reviewId")
	event.Payload["gitwebUrl"] = "https://git.test/?p=kernel"
	if got := event.ReviewURL("https://app.test"); got != "https://git.test/?p=kernel" {
		t.Errorf("ReviewURL without a review id = %q", got)
	}

	delete(event.Payload, "gitwebUrl")
	if got := event.ReviewURL("https://app.test"); got != "" {
		t.Errorf("ReviewURL with nothing to link to = %q, want empty", got)
	}
}

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

func TestEnvHostListTreatsStarAsEveryHost(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty allows every host", raw: "", want: nil},
		{name: "a star allows every host", raw: "*", want: nil},
		{name: "a star anywhere in the list wins", raw: "chat.company.tld, *", want: nil},
		{name: "a star with spaces still wins", raw: "  *  ", want: nil},
		{name: "hosts are lowercased and trimmed", raw: " Chat.Company.TLD , hooks.test ", want: []string{"chat.company.tld", "hooks.test"}},
		{name: "a wildcard prefix is dropped", raw: "*.company.tld", want: []string{"company.tld"}},
		{name: "empty entries are ignored", raw: "a.test,,b.test", want: []string{"a.test", "b.test"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("TEST_WEBHOOK_ALLOWED_HOSTS", testCase.raw)
			got := envHostList("TEST_WEBHOOK_ALLOWED_HOSTS")

			if len(got) != len(testCase.want) {
				t.Fatalf("envHostList(%q) = %v, want %v", testCase.raw, got, testCase.want)
			}
			for index, host := range testCase.want {
				if got[index] != host {
					t.Fatalf("envHostList(%q) = %v, want %v", testCase.raw, got, testCase.want)
				}
			}
		})
	}
}

// A star must reach the dispatcher as "no restriction", not as a host nobody
// can ever match.
func TestStarAllowsAnyTarget(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_ALLOWED_HOSTS", "*")
	allowed := envHostList("TEST_WEBHOOK_ALLOWED_HOSTS")

	for _, target := range []string{"https://anything.test/hook", "http://10.0.0.1:9000/hook"} {
		if _, err := ValidateTargetURL(target, allowed); err != nil {
			t.Errorf("ValidateTargetURL(%q) with a star list = %v, want no error", target, err)
		}
	}
}

func TestValidateTargetURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		allowed []string
		wantErr bool
	}{
		{name: "https is accepted", url: "https://example.test/hook"},
		{name: "http is accepted", url: "http://example.test/hook"},
		{name: "other schemes are refused", url: "file:///etc/passwd", wantErr: true},
		{name: "a missing host is refused", url: "https:///hook", wantErr: true},
		{name: "an empty url is refused", url: "  ", wantErr: true},
		{name: "an allowed host passes", url: "https://hooks.example.test/x", allowed: []string{"example.test"}},
		{name: "an exact allowed host passes", url: "https://example.test/x", allowed: []string{"example.test"}},
		{name: "a host outside the list is refused", url: "https://evil.test/x", allowed: []string{"example.test"}, wantErr: true},
		{name: "a lookalike suffix is refused", url: "https://notexample.test/x", allowed: []string{"example.test"}, wantErr: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ValidateTargetURL(testCase.url, testCase.allowed)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("ValidateTargetURL(%q) error = %v, wantErr %v", testCase.url, err, testCase.wantErr)
			}
		})
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
