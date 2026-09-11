package main

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config holds every knob the relay reads from the environment at startup.
type Config struct {
	RedisURL     string
	RedisChannel string
	FrontendURL  string
	ListenAddr   string
	DryRun       bool

	// Delivery
	Workers        int
	QueueSize      int
	Timeout        time.Duration
	MaxAttempts    int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
	UserAgent      string

	// Target restrictions
	AllowedHosts         []string
	BlockPrivateNetworks bool
}

var truthy = regexp.MustCompile(`^(?i)(1|true|yes|on)$`)

func parseBoolean(value string) bool {
	return truthy.MatchString(strings.TrimSpace(value))
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}

	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	return parseBoolean(value)
}

func envInt(key string, fallback, min, max int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value < min || value > max {
		return fallback
	}

	return value
}

func envDuration(key string, fallback, min, max time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}

	// Bare numbers are read as milliseconds so the variables stay readable
	// from docker-compose, where "5000" is friendlier than "5s".
	if milliseconds, err := strconv.Atoi(raw); err == nil {
		return clampDuration(time.Duration(milliseconds)*time.Millisecond, fallback, min, max)
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}

	return clampDuration(parsed, fallback, min, max)
}

func clampDuration(value, fallback, min, max time.Duration) time.Duration {
	if value < min || value > max {
		return fallback
	}

	return value
}

// envHostList reads a comma-separated host allow-list, and returns nil when
// every host is allowed.
//
// "*" spells that explicitly, the way the frontend, backend and websocket
// relay allow-lists already do, so the whole stack reads the same; an empty
// value means the same thing. A "*." prefix on an entry is accepted and
// dropped, since "*.company.tld" and "company.tld" both mean the domain and
// its subdomains here.
func envHostList(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}

	hosts := make([]string, 0, 4)
	for _, entry := range strings.Split(raw, ",") {
		host := strings.ToLower(strings.TrimSpace(entry))
		switch {
		case host == "":
			continue
		case host == "*":
			return nil
		default:
			hosts = append(hosts, strings.TrimPrefix(host, "*."))
		}
	}

	return hosts
}

// LoadConfig reads the relay configuration, applying the same defaults the
// sibling IRC and email relays use where the setting is shared.
func LoadConfig() (Config, error) {
	config := Config{
		RedisURL:     envString("REDIS_URL", "redis://localhost:6379"),
		RedisChannel: envString("REDIS_CHANNEL", "notifications:webhook"),
		FrontendURL:  strings.TrimRight(envString("FRONTEND_URL", "http://localhost:5173"), "/"),
		ListenAddr:   fmt.Sprintf(":%d", envInt("PORT", 3002, 1, 65535)),
		DryRun:       envBool("HTTP_RELAY_DRY_RUN", false),

		Workers:        envInt("WEBHOOK_WORKERS", 4, 1, 256),
		QueueSize:      envInt("WEBHOOK_QUEUE_SIZE", 1024, 1, 100000),
		Timeout:        envDuration("WEBHOOK_TIMEOUT", 10*time.Second, time.Second, 2*time.Minute),
		MaxAttempts:    envInt("WEBHOOK_MAX_ATTEMPTS", 3, 1, 10),
		RetryBaseDelay: envDuration("WEBHOOK_RETRY_BASE_DELAY", time.Second, 100*time.Millisecond, time.Minute),
		RetryMaxDelay:  envDuration("WEBHOOK_RETRY_MAX_DELAY", 30*time.Second, time.Second, 10*time.Minute),
		UserAgent:      envString("WEBHOOK_USER_AGENT", "git-web-review-http-relay/1.0"),

		AllowedHosts:         envHostList("WEBHOOK_ALLOWED_HOSTS"),
		BlockPrivateNetworks: envBool("WEBHOOK_BLOCK_PRIVATE_NETWORKS", false),
	}

	if _, err := url.Parse(config.RedisURL); err != nil {
		return Config{}, fmt.Errorf("invalid REDIS_URL: %w", err)
	}

	if config.RetryMaxDelay < config.RetryBaseDelay {
		config.RetryMaxDelay = config.RetryBaseDelay
	}

	return config, nil
}
