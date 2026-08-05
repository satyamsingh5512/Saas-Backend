package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// resendEndpoint is the Resend send-email API. Declared as a var so tests can
// point it at a local httptest server without a network call.
var resendEndpoint = "https://api.resend.com/emails"

// Resend delivers mail through the Resend HTTP API.
type Resend struct {
	apiKey string
	from   string
	client *http.Client
}

// NewResend builds a Resend sender. from must be a sender on a domain verified
// in Resend ("Acme <noreply@acme.com>" or a bare address); Resend rejects
// anything else, and it does so per-message rather than at configuration time.
func NewResend(apiKey, from string) *Resend {
	return &Resend{
		apiKey: apiKey,
		from:   from,
		client: &http.Client{
			// A hung provider must not hold a goroutine open indefinitely. The
			// caller's context normally bounds this too, but the timeout is set
			// here as well so a Sender constructed without one is still safe.
			Timeout: 15 * time.Second,
		},
	}
}

type resendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text,omitempty"`
	HTML    string   `json:"html,omitempty"`
	Tags    []tag    `json:"tags,omitempty"`
}

type tag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Send posts one message to Resend.
//
// Errors carry the provider's status code and response body, which is safe: a
// Resend error names the rejected sender or recipient, never the message body,
// so a token cannot reach the logs through this path.
func (r *Resend) Send(ctx context.Context, msg Message) error {
	if msg.To == "" {
		return fmt.Errorf("mailer: recipient is empty")
	}

	payload := resendRequest{
		From:    r.from,
		To:      []string{msg.To},
		Subject: msg.Subject,
		Text:    msg.Text,
		HTML:    msg.HTML,
	}
	if msg.Tag != "" {
		payload.Tags = []tag{{Name: "flow", Value: msg.Tag}}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("mailer: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resendEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mailer: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("mailer: send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Bounded read: a provider is not trusted to keep an error body small.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("mailer: resend returned %d: %s", resp.StatusCode, bytes.TrimSpace(detail))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}
