package mailer

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureSender records what a Notifier produced, so link construction and
// escaping can be asserted without a provider.
type captureSender struct {
	mu   sync.Mutex
	sent []Message
	done chan struct{}
}

func newCapture() *captureSender {
	return &captureSender{done: make(chan struct{}, 8)}
}

func (c *captureSender) Send(_ context.Context, msg Message) error {
	c.mu.Lock()
	c.sent = append(c.sent, msg)
	c.mu.Unlock()
	c.done <- struct{}{}
	return nil
}

// wait blocks for one dispatched message. Notifier sends on a detached
// goroutine, so a test that read c.sent directly would race it.
func (c *captureSender) wait(t *testing.T) Message {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		t.Fatal("no message dispatched")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sent[len(c.sent)-1]
}

func TestNotifierBuildsAbsoluteDashboardLinks(t *testing.T) {
	cap := newCapture()
	n := NewNotifier(cap, "https://app.example.com/", slog.New(slog.DiscardHandler))

	cases := []struct {
		name string
		send func()
		want string
	}{
		{"invitation", func() {
			n.SendInvitation(context.Background(), "invitee@example.com", "Acme Inc", "tok-invite", time.Now().Add(time.Hour))
		}, "https://app.example.com/app?invite=tok-invite"},
		{"password reset", func() {
			n.SendPasswordReset(context.Background(), "user@example.com", "tok-reset")
		}, "https://app.example.com/app?reset=tok-reset"},
		{"email verification", func() {
			n.SendEmailVerification(context.Background(), "user@example.com", "tok-verify")
		}, "https://app.example.com/app?verify=tok-verify"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.send()
			msg := cap.wait(t)
			if !strings.Contains(msg.Text, tc.want) {
				t.Errorf("text body missing %q:\n%s", tc.want, msg.Text)
			}
			if !strings.Contains(msg.HTML, tc.want) {
				t.Errorf("html body missing %q", tc.want)
			}
			// Both bodies are always populated: Resend needs one, and a client
			// that refuses HTML must not receive an empty message.
			if msg.Text == "" || msg.HTML == "" {
				t.Error("both text and html bodies must be set")
			}
			if msg.Subject == "" || msg.Tag == "" {
				t.Error("subject and tag must be set")
			}
		})
	}
}

// A tenant picks its own organization name, so it reaches the template as
// untrusted input. Mail clients render enough HTML for that to matter.
func TestNotifierEscapesTenantSuppliedName(t *testing.T) {
	cap := newCapture()
	n := NewNotifier(cap, "https://app.example.com", slog.New(slog.DiscardHandler))

	n.SendInvitation(context.Background(), "invitee@example.com",
		`<script>alert(1)</script>`, "tok", time.Now().Add(time.Hour))
	msg := cap.wait(t)

	if strings.Contains(msg.HTML, "<script>") {
		t.Errorf("organization name was not escaped:\n%s", msg.HTML)
	}
	if !strings.Contains(msg.HTML, "&lt;script&gt;") {
		t.Error("expected the escaped name to still appear in the body")
	}
}

// The dispatch goroutine must not inherit the request's cancellation: the HTTP
// handler returns before the provider call completes, which would cancel every
// send if the request context were used directly.
func TestNotifierSendsAfterCallerContextIsCancelled(t *testing.T) {
	cap := newCapture()
	n := NewNotifier(cap, "https://app.example.com", slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(context.Background())
	n.SendPasswordReset(ctx, "user@example.com", "tok")
	cancel()

	msg := cap.wait(t)
	if msg.To != "user@example.com" {
		t.Errorf("unexpected recipient %q", msg.To)
	}
}

func TestNoopSenderNeverFails(t *testing.T) {
	if err := (Noop{Logger: slog.New(slog.DiscardHandler)}).Send(context.Background(), Message{To: "a@b.c"}); err != nil {
		t.Fatalf("noop sender returned %v", err)
	}
}

func TestResendPostsExpectedPayload(t *testing.T) {
	var (
		gotAuth        string
		gotContentType string
		gotBody        resendRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1"}`))
	}))
	defer srv.Close()

	original := resendEndpoint
	resendEndpoint = srv.URL
	defer func() { resendEndpoint = original }()

	sender := NewResend("re_test_key", "Acme <noreply@acme.test>")
	err := sender.Send(context.Background(), Message{
		To: "user@example.com", Subject: "Reset your password",
		Text: "text body", HTML: "<p>html body</p>", Tag: "password-reset",
	})
	if err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}

	if gotAuth != "Bearer re_test_key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	if gotBody.From != "Acme <noreply@acme.test>" {
		t.Errorf("from = %q", gotBody.From)
	}
	if len(gotBody.To) != 1 || gotBody.To[0] != "user@example.com" {
		t.Errorf("to = %v", gotBody.To)
	}
	if gotBody.Subject != "Reset your password" || gotBody.Text != "text body" || gotBody.HTML != "<p>html body</p>" {
		t.Errorf("unexpected body %+v", gotBody)
	}
	if len(gotBody.Tags) != 1 || gotBody.Tags[0].Value != "password-reset" {
		t.Errorf("tags = %+v", gotBody.Tags)
	}
}

// A provider rejection must surface as an error the caller logs, not a silent
// success that looks like a delivered message.
func TestResendReportsProviderRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"domain is not verified"}`))
	}))
	defer srv.Close()

	original := resendEndpoint
	resendEndpoint = srv.URL
	defer func() { resendEndpoint = original }()

	err := NewResend("re_test_key", "noreply@unverified.test").
		Send(context.Background(), Message{To: "user@example.com", Subject: "s", Text: "t"})
	if err == nil {
		t.Fatal("Send() = nil, want error")
	}
	if !strings.Contains(err.Error(), "422") || !strings.Contains(err.Error(), "domain is not verified") {
		t.Errorf("error should carry provider status and detail, got %v", err)
	}
}

func TestResendRejectsEmptyRecipient(t *testing.T) {
	if err := NewResend("k", "f@x.test").Send(context.Background(), Message{Subject: "s"}); err == nil {
		t.Fatal("Send() with no recipient = nil, want error")
	}
}
