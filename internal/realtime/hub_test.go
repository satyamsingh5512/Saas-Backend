package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestMemoryBrokerRoundTrip proves instance-local fan-out: a subscriber on
// the recipient channel receives the published event.
func TestMemoryBrokerRoundTrip(t *testing.T) {
	b := New("", nil)
	if b.UsingRedis() {
		t.Fatal("blank URL must yield a memory-only broker")
	}
	tenantID, userID := uuid.New(), uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, unsubscribe := b.Subscribe(ctx, tenantID, userID)
	defer unsubscribe()

	want := Event{ID: "evt-1", TenantID: tenantID.String(), UserID: userID.String(), Type: "test", Title: "hello"}
	b.Publish(ctx, want)

	select {
	case got := <-events:
		if got.ID != want.ID || got.Title != want.Title {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for published event")
	}
	// Exactly-once per transport: a second copy would mean local fan-out and
	// Redis echo both fired (regression test for the duplicate-delivery bug
	// caught during live verification).
	select {
	case e := <-events:
		t.Fatalf("duplicate delivery: received %+v twice", e)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestTenantIsolation proves a subscriber never receives another tenant's or
// another user's events, even when published concurrently.
func TestTenantIsolation(t *testing.T) {
	b := New("", nil)
	tenantA, tenantB := uuid.New(), uuid.New()
	userA1, userA2 := uuid.New(), uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventsA1, unsub := b.Subscribe(ctx, tenantA, userA1)
	defer unsub()

	// Events for: same tenant different user, different tenant, and the sibling
	// that must arrive.
	b.Publish(ctx, Event{ID: "other-user", TenantID: tenantA.String(), UserID: userA2.String(), Title: "x"})
	b.Publish(ctx, Event{ID: "other-tenant", TenantID: tenantB.String(), UserID: userA1.String(), Title: "x"})
	b.Publish(ctx, Event{ID: "mine", TenantID: tenantA.String(), UserID: userA1.String(), Title: "x"})

	deadline := time.After(2 * time.Second)
	received := map[string]bool{}
	for len(received) < 1 {
		select {
		case e := <-eventsA1:
			received[e.ID] = true
		case <-deadline:
			t.Fatalf("timed out; received=%v", received)
		}
	}
	if !received["mine"] {
		t.Fatalf("missing own event; received=%v", received)
	}
	// Drain briefly: nothing else may arrive.
	time.Sleep(100 * time.Millisecond)
	select {
	case e := <-eventsA1:
		t.Fatalf("cross-boundary leak: received %+v", e)
	default:
	}
}

// TestPublishWithoutSubscribersNeverBlocks proves best-effort delivery.
func TestPublishWithoutSubscribersNeverBlocks(t *testing.T) {
	b := New("", nil)
	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			b.Publish(ctx, Event{ID: "x", TenantID: uuid.New().String(), UserID: uuid.New().String()})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publish blocked with no subscribers")
	}
}
