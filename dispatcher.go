package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// discardLimit caps how much of a webhook response is drained. Reading the
// body keeps the connection reusable; the content itself is of no interest.
const discardLimit = 8 << 10

// Stats counts what the relay did, and is exposed on the HTTP status endpoint.
type Stats struct {
	Received      atomic.Int64
	Skipped       atomic.Int64
	InvalidTarget atomic.Int64
	Queued        atomic.Int64
	Dropped       atomic.Int64
	Delivered     atomic.Int64
	Failed        atomic.Int64
	Retried       atomic.Int64
	EncodeFailed  atomic.Int64
}

// Snapshot returns the counters as a JSON-friendly map.
func (stats *Stats) Snapshot() map[string]int64 {
	return map[string]int64{
		"received":      stats.Received.Load(),
		"skipped":       stats.Skipped.Load(),
		"invalidTarget": stats.InvalidTarget.Load(),
		"queued":        stats.Queued.Load(),
		"dropped":       stats.Dropped.Load(),
		"delivered":     stats.Delivered.Load(),
		"failed":        stats.Failed.Load(),
		"retried":       stats.Retried.Load(),
		"encodeFailed":  stats.EncodeFailed.Load(),
	}
}

type delivery struct {
	target         *url.URL
	body           []byte
	eventType      string
	notificationID string
	deliveryID     string
	userID         string
}

// Dispatcher POSTs incoming events to each recipient's webhook URL through a
// bounded worker pool.
type Dispatcher struct {
	config   Config
	client   *http.Client
	queue    chan delivery
	workers  sync.WaitGroup
	stats    Stats
	closeOne sync.Once
}

// NewDispatcher builds the delivery pipeline but does not start it.
func NewDispatcher(config Config) *Dispatcher {
	return &Dispatcher{
		config: config,
		client: newHTTPClient(config),
		queue:  make(chan delivery, config.QueueSize),
	}
}

func newHTTPClient(config Config) *http.Client {
	dialer := &net.Dialer{Timeout: config.Timeout, KeepAlive: 30 * time.Second}
	if config.BlockPrivateNetworks {
		// Control runs after DNS resolution with the address actually about to
		// be dialled, so a hostname that resolves to a private IP is caught
		// here rather than trusted from the URL.
		dialer.Control = func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("cannot parse dial address %q: %w", address, err)
			}

			if isPrivateAddress(net.ParseIP(host)) {
				return fmt.Errorf("refusing to reach private address %s", host)
			}

			return nil
		}
	}

	return &http.Client{
		Timeout: config.Timeout,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: config.Timeout,
		},
		// A notification must land on the URL the user configured, so a
		// redirect to somewhere else is refused rather than followed.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Stats exposes the live counters.
func (dispatcher *Dispatcher) Stats() *Stats {
	return &dispatcher.stats
}

// Start launches the worker pool. Workers stop once Close is called and the
// queue is drained, or as soon as ctx is cancelled.
func (dispatcher *Dispatcher) Start(ctx context.Context) {
	for worker := 0; worker < dispatcher.config.Workers; worker++ {
		dispatcher.workers.Add(1)
		go func() {
			defer dispatcher.workers.Done()
			for job := range dispatcher.queue {
				dispatcher.deliver(ctx, job)
			}
		}()
	}
}

// Close stops accepting new events and waits for the queued ones to drain.
func (dispatcher *Dispatcher) Close() {
	dispatcher.closeOne.Do(func() { close(dispatcher.queue) })
	dispatcher.workers.Wait()
}

// Enqueue prepares a delivery and hands it to the workers. Events for users
// who have webhooks disabled are dropped here, before any encoding work.
func (dispatcher *Dispatcher) Enqueue(event *Event) {
	dispatcher.stats.Received.Add(1)

	if !event.User.WebhookNotificationsEnabled || event.User.WebhookURL == "" {
		dispatcher.stats.Skipped.Add(1)
		return
	}

	target, err := ValidateTargetURL(event.User.WebhookURL, dispatcher.config.AllowedHosts)
	if err != nil {
		dispatcher.stats.InvalidTarget.Add(1)
		log.Printf("webhook target rejected for user %s: %v", event.User.ID, err)
		return
	}

	deliveryID := NewDeliveryID()
	body, err := json.Marshal(NewWebhookPayload(event, dispatcher.config.FrontendURL, deliveryID))
	if err != nil {
		dispatcher.stats.EncodeFailed.Add(1)
		log.Printf("cannot encode notification %s: %v", event.NotificationID, err)
		return
	}

	job := delivery{
		target:         target,
		body:           body,
		eventType:      event.Type,
		notificationID: event.NotificationID,
		deliveryID:     deliveryID,
		userID:         event.User.ID,
	}

	if dispatcher.config.DryRun {
		log.Printf("[dry-run] POST %s (%s for user %s): %s", target.Redacted(), job.eventType, job.userID, body)
		dispatcher.stats.Delivered.Add(1)
		return
	}

	select {
	case dispatcher.queue <- job:
		dispatcher.stats.Queued.Add(1)
	default:
		dispatcher.stats.Dropped.Add(1)
		log.Printf("webhook queue is full, dropping %s for user %s", job.eventType, job.userID)
	}
}

// deliver POSTs one job, retrying on transport errors and on the status codes
// that mean "try again later".
func (dispatcher *Dispatcher) deliver(ctx context.Context, job delivery) {
	for attempt := 1; attempt <= dispatcher.config.MaxAttempts; attempt++ {
		retryAfter, err := dispatcher.post(ctx, job, attempt)
		if err == nil {
			dispatcher.stats.Delivered.Add(1)
			return
		}

		var permanent permanentError
		if errors.As(err, &permanent) || attempt == dispatcher.config.MaxAttempts {
			dispatcher.stats.Failed.Add(1)
			log.Printf("webhook delivery %s failed after %d attempt(s): %v", job.deliveryID, attempt, err)
			return
		}

		wait := dispatcher.backoff(attempt)
		if retryAfter > wait {
			wait = min(retryAfter, dispatcher.config.RetryMaxDelay)
		}

		dispatcher.stats.Retried.Add(1)
		log.Printf("webhook delivery %s attempt %d failed, retrying in %s: %v", job.deliveryID, attempt, wait, err)

		select {
		case <-ctx.Done():
			dispatcher.stats.Failed.Add(1)
			return
		case <-time.After(wait):
		}
	}
}

// permanentError marks a response the endpoint will answer the same way on a
// retry, such as a 404 on a deleted webhook.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func (dispatcher *Dispatcher) post(ctx context.Context, job delivery, attempt int) (time.Duration, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, job.target.String(), bytes.NewReader(job.body))
	if err != nil {
		return 0, permanentError{err}
	}

	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", dispatcher.config.UserAgent)
	request.Header.Set("X-Git-Web-Review-Event", job.eventType)
	request.Header.Set("X-Git-Web-Review-Notification-Id", job.notificationID)
	request.Header.Set("X-Git-Web-Review-Delivery", job.deliveryID)
	request.Header.Set("X-Git-Web-Review-Attempt", strconv.Itoa(attempt))

	response, err := dispatcher.client.Do(request)
	if err != nil {
		return 0, err
	}

	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, discardLimit))

	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return 0, nil
	}

	statusError := fmt.Errorf("%s answered %s", job.target.Redacted(), response.Status)
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
		return parseRetryAfter(response.Header.Get("Retry-After")), statusError
	}

	return 0, permanentError{statusError}
}

// backoff grows exponentially and carries jitter so a shared endpoint does not
// see every pending delivery retry at the same instant.
func (dispatcher *Dispatcher) backoff(attempt int) time.Duration {
	wait := dispatcher.config.RetryBaseDelay << (attempt - 1)
	if wait > dispatcher.config.RetryMaxDelay || wait <= 0 {
		wait = dispatcher.config.RetryMaxDelay
	}

	return wait + time.Duration(rand.Int63n(int64(wait/4)+1))
}

// parseRetryAfter reads the header in both its delay-seconds and HTTP-date
// forms, and ignores anything else.
func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}

	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}

	if date, err := http.ParseTime(value); err == nil {
		if wait := time.Until(date); wait > 0 {
			return wait
		}
	}

	return 0
}
