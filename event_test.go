package main

import (
	"testing"
)

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
