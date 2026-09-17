package main

import (
	"net"
	"testing"
)

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

func TestIsPrivateAddress(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "0.0.0.0", "10.0.0.1", "172.16.0.1", "172.31.255.255",
		"192.168.1.5", "169.254.169.254", "100.64.0.1", "100.127.255.255",
		"224.0.0.1", "::1", "::", "fe80::1", "fc00::1", "fd12:3456::1",
		"ff02::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1",
	}
	for _, address := range blocked {
		if !isPrivateAddress(net.ParseIP(address)) {
			t.Errorf("isPrivateAddress(%q) = false, want true", address)
		}
	}

	allowed := []string{
		"8.8.8.8", "1.1.1.1", "172.15.0.1", "172.32.0.1", "192.169.0.1",
		"100.63.255.255", "100.128.0.1", "223.255.255.255",
		"2606:4700:4700::1111", "::ffff:8.8.8.8",
	}
	for _, address := range allowed {
		if isPrivateAddress(net.ParseIP(address)) {
			t.Errorf("isPrivateAddress(%q) = true, want false", address)
		}
	}

	if !isPrivateAddress(nil) {
		t.Error("isPrivateAddress(nil) = false, want true: an unparsable address must not be dialled")
	}
}
