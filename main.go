package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
)

// shutdownGrace is how long in-flight deliveries and the HTTP server get to
// finish once a termination signal arrives.
const shutdownGrace = 15 * time.Second

// healthPingInterval is how often the Redis connection is probed so /health
// reflects a subscription that dropped between two events.
const healthPingInterval = 10 * time.Second

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	if err := run(); err != nil {
		log.Fatalf("http-relay stopped: %v", err)
	}
}

func run() error {
	startedAt := time.Now()

	config, err := LoadConfig()
	if err != nil {
		return err
	}

	options, err := redis.ParseURL(config.RedisURL)
	if err != nil {
		return err
	}

	// The signal context stops the subscription; deliveries keep their own
	// context so the ones already queued still get their grace period.
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	deliveryCtx, stopDeliveries := context.WithCancel(context.Background())
	defer stopDeliveries()

	dispatcher := NewDispatcher(config)
	dispatcher.Start(deliveryCtx)

	client := redis.NewClient(options)
	defer client.Close()

	health := &healthState{}
	server := NewServer(config, dispatcher.Stats(), health, startedAt)
	serverErrors := make(chan error, 1)

	go func() {
		log.Printf("http-relay listening on %s", config.ListenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	subscription := client.Subscribe(signalCtx, config.RedisChannel)
	defer subscription.Close()

	if _, err := subscription.Receive(signalCtx); err != nil {
		shutdown(server, dispatcher, stopDeliveries)
		return err
	}

	health.redisConnected.Store(true)
	go watchRedis(signalCtx, client, health)

	log.Printf(
		"http-relay subscribed to Redis channel %s with %d worker(s)%s",
		config.RedisChannel,
		config.Workers,
		dryRunSuffix(config.DryRun),
	)

	messages := subscription.Channel()
	for {
		select {
		case <-signalCtx.Done():
			log.Printf("shutting down")
			shutdown(server, dispatcher, stopDeliveries)
			return nil
		case err := <-serverErrors:
			shutdown(server, dispatcher, stopDeliveries)
			return err
		case message, ok := <-messages:
			if !ok {
				shutdown(server, dispatcher, stopDeliveries)
				return errors.New("redis subscription closed")
			}

			event, err := ParseEvent([]byte(message.Payload))
			if err != nil {
				log.Printf("ignoring message on %s: %v", message.Channel, err)
				continue
			}

			dispatcher.Enqueue(event)
		}
	}
}

// watchRedis keeps the health endpoint honest about the subscription, which
// go-redis re-establishes on its own after a connection drop.
func watchRedis(ctx context.Context, client *redis.Client, health *healthState) {
	ticker := time.NewTicker(healthPingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, healthPingInterval)
			err := client.Ping(pingCtx).Err()
			cancel()

			connected := err == nil
			if health.redisConnected.Swap(connected) != connected {
				if connected {
					log.Printf("redis connection restored")
				} else {
					log.Printf("redis connection lost: %v", err)
				}
			}
		}
	}
}

// shutdown drains the queued deliveries first, then closes the HTTP server,
// so /health keeps answering while the backlog is flushed.
func shutdown(server *http.Server, dispatcher *Dispatcher, stopDeliveries context.CancelFunc) {
	drained := make(chan struct{})
	go func() {
		dispatcher.Close()
		close(drained)
	}()

	select {
	case <-drained:
	case <-time.After(shutdownGrace):
		log.Printf("giving up on %s of pending deliveries", shutdownGrace)
	}

	stopDeliveries()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("http server shutdown: %v", err)
	}
}

func dryRunSuffix(dryRun bool) string {
	if dryRun {
		return " in dry-run mode"
	}

	return ""
}
