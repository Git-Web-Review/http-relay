package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// EventUser is the user slice the backend embeds in every notification event.
// Only the webhook fields are relay-specific; the rest mirrors the IRC and
// email relays so one Redis payload shape serves all three.
type EventUser struct {
	ID                          string `json:"id"`
	Email                       string `json:"email"`
	Nickname                    string `json:"nickname"`
	Locale                      string `json:"locale"`
	WebhookNotificationsEnabled bool   `json:"webhookNotificationsEnabled"`
	WebhookURL                  string `json:"webhookUrl"`
}

// Event is one notification published on the Redis channel.
type Event struct {
	Type           string         `json:"type"`
	NotificationID string         `json:"notificationId"`
	CreatedAt      string         `json:"createdAt"`
	User           EventUser      `json:"user"`
	Payload        map[string]any `json:"payload"`
}

// payloadString reads a payload field only when it carries a non-blank string.
func payloadString(payload map[string]any, key string) string {
	value, ok := payload[key].(string)
	if !ok {
		return ""
	}

	return strings.TrimSpace(value)
}

// ParseEvent decodes one Redis message. It rejects anything that cannot be
// delivered rather than letting a malformed event reach the HTTP client.
func ParseEvent(message []byte) (*Event, error) {
	var event Event
	if err := json.Unmarshal(message, &event); err != nil {
		return nil, fmt.Errorf("invalid notification event: %w", err)
	}

	if event.User.ID == "" {
		return nil, fmt.Errorf("notification event is missing its user")
	}

	if event.Payload == nil {
		event.Payload = map[string]any{}
	}

	return &event, nil
}

// ReviewURL is the one thing the relay adds that the payload cannot carry on
// its own: the payload holds a review id, and turning it into a link needs the
// frontend base URL, which only the relay is configured with.
func (event *Event) ReviewURL(frontendURL string) string {
	if reviewID := payloadString(event.Payload, "reviewId"); reviewID != "" {
		return fmt.Sprintf("%s/review/%s", strings.TrimRight(frontendURL, "/"), url.PathEscape(reviewID))
	}

	return payloadString(event.Payload, "gitwebUrl")
}
