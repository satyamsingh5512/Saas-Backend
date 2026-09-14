// Package realtime delivers live notification updates to browsers over
// Server-Sent Events (SSE), fanned out through Redis Pub/Sub when Redis is
// configured.
//
// Why SSE and not WebSockets: notification flow is server-to-client only --
// the client never sends frames back -- so the full-duplex machinery of
// WebSockets buys nothing. SSE works over plain HTTP (no Upgrade dance),
// is replayable with Last-Event-ID, and passes through nginx with a single
// buffering directive. Why Redis Pub/Sub and not Kafka/RabbitMQ: the fan-out
// domain is one deployment's app instances, which Redis already serves as
// the permission cache; adding a second broker would double the stateful
// services on a 2-OCPU free-tier VM for no delivery guarantee SSE needs
// (missed events are refetched from the REST list endpoint on reconnect).
//
// Tenant isolation is structural, not conventional: the pub/sub channel is
// derived from the authenticated caller's own tenant+user IDs
// ("ntfy:<tenant>:<user>"), a subscriber can only subscribe to its own
// channel, and the publisher addresses events to the recipient's channel.
// There is no "subscribe to everything" code path to misuse.
package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Event is the payload streamed to one subscriber.
type Event struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	UserID    string    `json:"user_id"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// channelFor derives the delivery channel for one recipient. Unexported so
// call sites cannot construct arbitrary channels.
func channelFor(tenantID, userID uuid.UUID) string {
	return "ntfy:" + tenantID.String() + ":" + userID.String()
}

// Broker fans notification events out to live SSE subscribers.
type Broker struct {
	redis  *redis.Client
	logger *slog.Logger

	mu   sync.RWMutex
	subs map[string]map[chan Event]struct{}
}

// New connects a lightweight Redis client for Pub/Sub when redisURL is set.
// A blank URL, malformed URL, or unreachable Redis yields a memory-only
// broker: correct on a single instance, with Redis re-checked on next deploy.
// Pub/Sub uses its own connection because Subscribe blocks a connection for
// the life of the subscription and must never starve the cache pool.
func New(redisURL string, logger *slog.Logger) *Broker {
	if logger == nil {
		logger = slog.Default()
	}
	b := &Broker{logger: logger, subs: make(map[string]map[chan Event]struct{})}
	redisURL = strings.TrimSpace(redisURL)
	if redisURL == "" {
		return b
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		logger.Warn("realtime: redis URL invalid, live fan-out is instance-local")
		return b
	}
	options.DialTimeout = 3 * time.Second
	options.ReadTimeout = 0 // subscriptions block indefinitely by design
	options.PoolSize = 4    // pub/sub only: 1 publisher + headroom for subscribers
	client := redis.NewClient(options)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		logger.Warn("realtime: redis unavailable, live fan-out is instance-local")
		_ = client.Close()
		return b
	}
	b.redis = client
	logger.Info("realtime: redis pub/sub fan-out enabled")
	return b
}

// UsingRedis reports whether cross-instance fan-out is active.
func (b *Broker) UsingRedis() bool { return b != nil && b.redis != nil }

// Close releases the Pub/Sub connection. Safe on nil.
func (b *Broker) Close() error {
	if b == nil || b.redis == nil {
		return nil
	}
	return b.redis.Close()
}

// Publish delivers e to its recipient's live subscribers on this instance
// and, when Redis is configured, to the same channel on every other instance.
// Publish never fails the caller: delivery is best-effort by design, because
// every event is durably stored in Postgres first (the notifications service
// persists before publishing) and a disconnected client refetches from the
// REST list endpoint on reconnect.
//
// Transport selection is exclusive, not additive: with Redis configured,
// delivery goes through Redis alone -- every instance (including this one)
// receives it via its subscriptions. Fanning out locally AND publishing
// would deliver twice to local subscribers (once per path), a bug this
// exclusivity structurally prevents. Without Redis, instance-local fan-out
// is the only path.
func (b *Broker) Publish(ctx context.Context, e Event) {
	if b == nil {
		return
	}
	if b.redis != nil {
		raw, err := json.Marshal(e)
		if err != nil {
			return
		}
		ch := channelFor(mustParseUUID(e.TenantID), mustParseUUID(e.UserID))
		if err := b.redis.Publish(ctx, ch, raw).Err(); err != nil {
			b.logger.Debug("realtime: redis publish failed; event remains in Postgres for refetch",
				slog.Any("error", err))
		}
		return
	}
	b.publishLocal(channelFor(mustParseUUID(e.TenantID), mustParseUUID(e.UserID)), e)
}

// publishLocal fans out to subscribers on this instance only, dropping (not
// blocking) on slow consumers: the client refetches missed events on
// reconnect, while backpressure here would let one stalled browser stall
// delivery for everyone.
func (b *Broker) publishLocal(ch string, e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for sub := range b.subs[ch] {
		select {
		case sub <- e:
		default:
		}
	}
}

// Subscribe registers a live subscription for exactly one tenant+user pair
// (always the authenticated caller's own IDs, supplied by the SSE handler).
// The returned channel receives Events until unsubscribe is called or ctx is
// cancelled. Buffer absorbs publish bursts so a single slow flush does not
// drop the connection's events.
func (b *Broker) Subscribe(ctx context.Context, tenantID, userID uuid.UUID) (<-chan Event, func()) {
	out := make(chan Event, 32)
	if b == nil {
		close(out)
		return out, func() {}
	}
	ch := channelFor(tenantID, userID)

	b.mu.Lock()
	if b.subs[ch] == nil {
		b.subs[ch] = make(map[chan Event]struct{})
	}
	b.subs[ch][out] = struct{}{}
	b.mu.Unlock()

	stopRedis := func() {}
	if b.redis != nil {
		stopRedis = b.forwardRedis(ctx, ch, out)
	}

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			stopRedis()
			b.mu.Lock()
			if set, ok := b.subs[ch]; ok {
				delete(set, out)
				if len(set) == 0 {
					delete(b.subs, ch)
				}
			}
			b.mu.Unlock()
		})
	}
	go func() {
		<-ctx.Done()
		unsubscribe()
	}()
	return out, unsubscribe
}

// forwardRedis bridges one Redis channel into the subscriber's outbox. A
// dedicated Pub/Sub connection per active SSE stream is the standard go-redis
// pattern; connections are closed on unsubscribe, and Publish running
// alongside Subscribe on the shared client is safe in go-redis.
func (b *Broker) forwardRedis(ctx context.Context, channel string, out chan<- Event) func() {
	subCtx, cancel := context.WithCancel(context.Background())
	pubsub := b.redis.Subscribe(subCtx, channel)
	stop := func() {
		cancel()
		_ = pubsub.Close()
	}
	go func() {
		defer stop()
		msgCh := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case <-subCtx.Done():
				return
			case msg, ok := <-msgCh:
				if !ok {
					return
				}
				var e Event
				if err := json.Unmarshal([]byte(msg.Payload), &e); err != nil {
					continue
				}
				select {
				case out <- e:
				case <-ctx.Done():
					return
				default:
					// Slow consumer: drop, client refetches on reconnect.
				}
			}
		}
	}()
	return stop
}

func mustParseUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return id
}
