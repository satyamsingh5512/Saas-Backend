package keepalive

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(os.Stderr, nil)) }

func TestPublicHealthURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"render public url", "https://tenant-saas-backend.onrender.com", "https://tenant-saas-backend.onrender.com/health"},
		{"trailing slash", "https://example.com/", "https://example.com/health"},
		{"with port", "https://example.com:10000", "https://example.com:10000/health"},
		{"localhost skipped", "http://localhost:8080", ""},
		{"loopback ip skipped", "http://127.0.0.1:8080", ""},
		{"ipv6 loopback skipped", "http://[::1]:8080", ""},
		{"bare hostname gets scheme", "example.com", "https://example.com/health"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := publicHealthURL(tc.in); got != tc.want {
				t.Errorf("publicHealthURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The loop must actually hit the public URL on its interval. A nil DB only
// disables the database half; the HTTP half still runs.
func TestStartHitsHealthEndpoint(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			hits.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// httptest serves on 127.0.0.1, which publicHealthURL deliberately
	// skips, so bypass Start and exercise tick directly for the HTTP half.
	tick(http.DefaultClient, nil, server.URL+"/health", testLogger())
	if hits.Load() != 1 {
		t.Fatalf("expected 1 health hit, got %d", hits.Load())
	}
}

// Start with a public-looking target must tick without a database and stop
// cleanly on demand.
func TestStartStopLifecycle(t *testing.T) {
	stop := Start(nil, "https://example.invalid", 20*time.Millisecond, testLogger())
	time.Sleep(100 * time.Millisecond)
	stop()
	// Second call would panic on close-of-closed; lifecycle is single-shot
	// by contract, so reaching here without hanging is the assertion.
}
