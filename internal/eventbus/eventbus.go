// Package eventbus will host the Kafka-backed domain event publisher
// (Phase 10 - Event-Driven Architecture). Until that phase is implemented,
// NoopPublisher lets every module that depends on an EventPublisher
// interface (identity, membership, authz, billing, ...) function correctly
// without Kafka configured -- publishing becomes a silent no-op rather than
// a hard dependency, matching the config package's "empty KAFKA_BROKERS
// disables event publishing gracefully" documented behavior.
package eventbus

import "context"

// Publisher is the interface every module depends on to emit domain
// events. Matches the shape each module's own local EventPublisher
// interface expects (Go's structural typing means NoopPublisher and the
// future Kafka-backed implementation satisfy all of them without an import
// dependency from those modules onto this package).
type Publisher interface {
	Publish(ctx context.Context, topic string, key string, payload any) error
}

// NoopPublisher discards every event. Used as the default wiring until the
// Kafka producer (Phase 10) replaces it in cmd/server/main.go.
type NoopPublisher struct{}

func (NoopPublisher) Publish(ctx context.Context, topic string, key string, payload any) error {
	return nil
}
