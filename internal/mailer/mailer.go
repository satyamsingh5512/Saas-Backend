// Package mailer delivers transactional email for the flows that mint a
// single-use token: member invitations, password resets and email verification.
//
// Two implementations satisfy Sender. Resend talks to the Resend HTTP API;
// Noop discards the message and logs that it was undeliverable. Which one is
// wired in depends only on whether RESEND_API_KEY is set, so a development
// environment needs no mail provider and no flow changes behaviour for want of
// one -- the token is still minted, stored and returned by the API exactly as
// before.
//
// Deliberately built on net/http rather than an SDK. The whole API surface used
// here is one POST with a JSON body, so a dependency would buy nothing and this
// project's baseline is that a dependency has to justify itself.
package mailer

import (
	"context"
	"log/slog"
)

// Message is one outbound email. Both a text and an HTML body are always sent:
// Resend requires at least one, and mail clients that refuse HTML would
// otherwise render an empty message containing the very link it exists to
// deliver.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
	// Tag labels the message for provider-side filtering and for this
	// package's own logs. It is the flow name, never anything tenant-supplied.
	Tag string
}

// Sender delivers a message. Implementations must treat Message.Text and
// Message.HTML as already-escaped final content.
//
// A Sender is expected to be safe for concurrent use: every caller invokes it
// from a detached goroutine.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// Noop is the Sender used when no provider is configured. It logs at WARN with
// the recipient and tag but never the body, because every message this package
// carries contains a single-use credential in its link.
type Noop struct {
	Logger *slog.Logger
}

func (n Noop) Send(_ context.Context, msg Message) error {
	logger := n.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("email not sent: no mail transport configured",
		slog.String("to", msg.To),
		slog.String("tag", msg.Tag),
		slog.String("subject", msg.Subject))
	return nil
}
