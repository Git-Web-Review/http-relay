package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// WebhookUser identifies the recipient. It carries who the notification is
// for, not how they are notified: the other transports' settings stay the
// backend's business and never reach the endpoint.
type WebhookUser struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Nickname string `json:"nickname"`
	Locale   string `json:"locale"`
}

// WebhookPayload is the JSON body POSTed to the configured endpoint.
//
// The relay does not format notifications, it forwards them: Payload is the
// backend's notification payload passed through untouched, so a field added
// there reaches endpoints without a relay release, and consumers decide how to
// present it.
type WebhookPayload struct {
	App            string         `json:"app"`
	Event          string         `json:"event"`
	NotificationID string         `json:"notificationId"`
	DeliveryID     string         `json:"deliveryId"`
	CreatedAt      string         `json:"createdAt"`
	SentAt         string         `json:"sentAt"`
	URL            string         `json:"url"`
	User           WebhookUser    `json:"user"`
	Payload        map[string]any `json:"payload"`
}

// NewWebhookPayload assembles the delivery body from one event.
func NewWebhookPayload(event *Event, frontendURL, deliveryID string) WebhookPayload {
	return WebhookPayload{
		App:            "git-web-review",
		Event:          event.Type,
		NotificationID: event.NotificationID,
		DeliveryID:     deliveryID,
		CreatedAt:      event.CreatedAt,
		SentAt:         time.Now().UTC().Format(time.RFC3339Nano),
		URL:            event.ReviewURL(frontendURL),
		User: WebhookUser{
			ID:       event.User.ID,
			Email:    event.User.Email,
			Nickname: event.User.Nickname,
			Locale:   event.User.Locale,
		},
		Payload: event.Payload,
	}
}

// NewDeliveryID returns the identifier echoed in the X-Git-Web-Review-Delivery
// header, so a retry is recognisable as the same delivery on the far end.
func NewDeliveryID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return ""
	}

	return hex.EncodeToString(buffer)
}

// ValidateTargetURL rejects anything the relay must not call: a non-HTTP
// scheme, a missing host, or a host outside WEBHOOK_ALLOWED_HOSTS when that
// allow-list is configured.
func ValidateTargetURL(raw string, allowedHosts []string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("webhook URL is empty")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("webhook URL is not a valid URL: %w", err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("webhook URL scheme %q is not supported", parsed.Scheme)
	}

	if parsed.Host == "" {
		return nil, fmt.Errorf("webhook URL has no host")
	}

	if len(allowedHosts) > 0 && !hostAllowed(parsed.Hostname(), allowedHosts) {
		return nil, fmt.Errorf("webhook host %q is not in WEBHOOK_ALLOWED_HOSTS", parsed.Hostname())
	}

	return parsed, nil
}

// hostAllowed matches an exact host or any subdomain of a listed domain.
func hostAllowed(host string, allowedHosts []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, allowed := range allowedHosts {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}

	return false
}

// isPrivateAddress reports whether an IP belongs to a range that should stay
// unreachable from a user-supplied webhook URL when the relay is exposed to
// untrusted users.
func isPrivateAddress(ip net.IP) bool {
	if ip == nil {
		return true
	}

	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}

	// 100.64.0.0/10, the carrier-grade NAT range, is not covered by IsPrivate.
	if ipv4 := ip.To4(); ipv4 != nil && ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127 {
		return true
	}

	return false
}
