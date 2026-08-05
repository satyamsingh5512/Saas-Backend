package mailer

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

// dispatchTimeout bounds one provider call made from a detached goroutine.
const dispatchTimeout = 20 * time.Second

// Notifier turns a domain event into a message and hands it to a Sender. It is
// the type the identity and invitations services depend on, through their own
// narrow local interfaces.
//
// Every method dispatches on a detached goroutine and returns immediately, for
// two reasons. The obvious one is latency: no caller should wait on a third
// party to render a response. The load-bearing one is that
// RequestPasswordReset deliberately behaves identically for a real and an
// unknown address, and an inline provider call would undo that -- a request for
// an account that exists would take a few hundred milliseconds longer than one
// that does not, which is an account-existence oracle measurable from outside.
//
// The consequence is that a delivery failure cannot be reported to the caller.
// It is logged at ERROR with the recipient and flow, and no flow treats mail as
// a precondition: the token is already stored, and the invite token is also
// returned in the API response so the dashboard can show a link directly.
type Notifier struct {
	sender  Sender
	baseURL string
	logger  *slog.Logger
}

// NewNotifier wires a Sender to the public base URL used to build links.
func NewNotifier(sender Sender, baseURL string, logger *slog.Logger) *Notifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &Notifier{sender: sender, baseURL: strings.TrimRight(baseURL, "/"), logger: logger}
}

// link builds an absolute dashboard URL carrying a single-use token.
//
// The token goes through url.QueryEscape even though GenerateOpaqueToken emits
// base64url, which needs no escaping: relying on the generator's alphabet would
// make this correct only by coincidence, and silently wrong if that alphabet
// ever changes.
func (n *Notifier) link(param, token string) string {
	return fmt.Sprintf("%s/app?%s=%s", n.baseURL, param, url.QueryEscape(token))
}

// dispatch sends on a detached goroutine.
//
// context.WithoutCancel keeps any request-scoped values (the request id the
// logger correlates on) while dropping the cancellation that fires as soon as
// the HTTP handler returns -- with the request's own context the provider call
// would be cancelled before it completed, essentially always.
func (n *Notifier) dispatch(ctx context.Context, msg Message) {
	if n == nil || n.sender == nil {
		return
	}
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), dispatchTimeout)
	go func() {
		defer cancel()
		if err := n.sender.Send(detached, msg); err != nil {
			n.logger.Error("failed to send email",
				slog.String("to", msg.To),
				slog.String("tag", msg.Tag),
				slog.Any("error", err))
		}
	}()
}

// SendInvitation delivers a member invitation link.
func (n *Notifier) SendInvitation(ctx context.Context, to, orgName, token string, expiresAt time.Time) {
	if n == nil {
		return
	}
	org := orgName
	if org == "" {
		org = "an organization"
	}
	link := n.link("invite", token)
	expiry := expiresAt.UTC().Format("2 January 2006 15:04 UTC")

	n.dispatch(ctx, Message{
		To:      to,
		Tag:     "member-invitation",
		Subject: fmt.Sprintf("You have been invited to %s", org),
		Text: fmt.Sprintf(`You have been invited to join %s.

Accept the invitation:
%s

This link works once and expires on %s. If you were not expecting it, you can ignore this message.
`, org, link, expiry),
		HTML: htmlBody(
			fmt.Sprintf("You have been invited to join %s.", org),
			"Accept invitation",
			link,
			fmt.Sprintf("This link works once and expires on %s. If you were not expecting it, you can ignore this message.", expiry),
		),
	})
}

// SendPasswordReset delivers a password-reset link.
func (n *Notifier) SendPasswordReset(ctx context.Context, to, token string) {
	if n == nil {
		return
	}
	link := n.link("reset", token)

	n.dispatch(ctx, Message{
		To:      to,
		Tag:     "password-reset",
		Subject: "Reset your password",
		Text: fmt.Sprintf(`A password reset was requested for this address.

Choose a new password:
%s

This link works once and expires shortly. If you did not request it, no action is needed: your current password still works and nothing has changed.
`, link),
		HTML: htmlBody(
			"A password reset was requested for this address.",
			"Choose a new password",
			link,
			"This link works once and expires shortly. If you did not request it, no action is needed: your current password still works and nothing has changed.",
		),
	})
}

// SendEmailVerification delivers an address-verification link.
func (n *Notifier) SendEmailVerification(ctx context.Context, to, token string) {
	if n == nil {
		return
	}
	link := n.link("verify", token)

	n.dispatch(ctx, Message{
		To:      to,
		Tag:     "email-verification",
		Subject: "Verify your email address",
		Text: fmt.Sprintf(`Confirm this address to finish setting up your account.

Verify your email:
%s

This link works once. If you did not create an account, you can ignore this message.
`, link),
		HTML: htmlBody(
			"Confirm this address to finish setting up your account.",
			"Verify your email",
			link,
			"This link works once. If you did not create an account, you can ignore this message.",
		),
	})
}

// htmlBody renders the one layout every message in this package uses.
//
// Interpolated values are escaped with html.EscapeString because one of them --
// the organization name -- is tenant-supplied, and mail clients execute enough
// HTML for an unescaped name to matter. The link is escaped too: it is built
// here, but escaping it unconditionally means a future caller-supplied URL
// cannot break out of the attribute.
//
// Styling is inline because that is the only thing mail clients reliably honour;
// the dashboard's no-inline-style rule comes from its CSP and does not apply to
// an email body. Kept to a system font stack and no images, so it renders with
// remote content blocked.
func htmlBody(lead, action, link, footer string) string {
	safeLink := html.EscapeString(link)
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<body style="margin:0;padding:24px;background:#f6f6f6;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#111111;">
  <div style="max-width:520px;margin:0 auto;background:#ffffff;border:1px solid #e4e4e4;border-radius:8px;padding:32px;">
    <p style="margin:0 0 20px;font-size:16px;line-height:1.5;">%s</p>
    <p style="margin:0 0 24px;">
      <a href="%s" style="display:inline-block;padding:12px 20px;background:#111111;color:#ffffff;text-decoration:none;border-radius:6px;font-size:15px;font-weight:600;">%s</a>
    </p>
    <p style="margin:0 0 20px;font-size:13px;line-height:1.6;color:#555555;">
      If the button does not work, paste this link into your browser:<br>
      <span style="word-break:break-all;">%s</span>
    </p>
    <p style="margin:0;font-size:13px;line-height:1.6;color:#555555;">%s</p>
  </div>
</body>
</html>
`, html.EscapeString(lead), safeLink, html.EscapeString(action), safeLink, html.EscapeString(footer))
}
