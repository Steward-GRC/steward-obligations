// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"

	email "github.com/Bugs5382/go-email"
)

// mailgunTimeout bounds a single Mailgun HTTP messages-API call. Mailgun is a
// remote provider (unlike the localhost render sidecar), so this is a real
// network budget rather than headroom against a wedged local process.
const mailgunTimeout = 15 * time.Second

// Mailgun regional API base URLs. Region "us" (the default) posts to the US
// endpoint; "eu" posts to the EU endpoint for accounts provisioned there.
const (
	mailgunBaseURLUS = "https://api.mailgun.net"
	mailgunBaseURLEU = "https://api.eu.mailgun.net"
)

// ErrMailgunAuth is returned (wrapped) when Mailgun rejects the API key
// (HTTP 401/403). It is a non-transient error on purpose: a bad or absent key
// is a configuration fault, so retrying the same request per-message is
// pointless. Slice C's circuit-breaker keys off this to open the pause-gate;
// callers test for it with errors.Is.
var ErrMailgunAuth = errors.New("mailgun: auth failed")

// MailgunConfig holds everything MailgunTransport needs to send. It is passed
// in by the constructor -- the transport never reads core/settings itself
// . Region is "us" (default) or "eu"; FromAddress is
// the envelope From applied when a Message carries none.
//
// APIKey is a write-only secret: it is used only as the HTTP Basic password and
// is never logged, never returned by any method, and never serialized out.
type MailgunConfig struct {
	APIKey      string
	Domain      string
	Region      string // "us" | "eu"; empty defaults to "us"
	FromAddress string
}

// MailgunTransport implements go-email's email.Transport by delivering through
// Mailgun's HTTP messages API (POST /v3/<domain>/messages), NOT SMTP. It
// classifies each response into a go-email disposition (success / transient /
// permanent) and tracks a coarse health signal the Slice-C circuit-breaker
// will consume; the breaker itself is out of scope for this task.
type MailgunTransport struct {
	cfg     MailgunConfig
	client  *http.Client
	baseURL string

	mu         sync.Mutex
	lastMsgID  string
	lastStatus int
	healthy    bool
}

// MailgunOption customizes a MailgunTransport at construction. WithMailgunBaseURL
// overrides the region-derived base URL (tests point it at an httptest server);
// WithMailgunHTTPClient supplies a custom *http.Client (tests inject a capturing
// RoundTripper).
type MailgunOption func(*MailgunTransport)

// WithMailgunBaseURL overrides the base URL the region would otherwise select.
// Primarily a test seam for pointing the transport at an httptest stub.
func WithMailgunBaseURL(u string) MailgunOption {
	return func(t *MailgunTransport) { t.baseURL = strings.TrimRight(u, "/") }
}

// WithMailgunHTTPClient overrides the default HTTP client (bounded timeout).
func WithMailgunHTTPClient(c *http.Client) MailgunOption {
	return func(t *MailgunTransport) {
		if c != nil {
			t.client = c
		}
	}
}

// NewMailgunTransport builds a MailgunTransport for cfg. The base URL is derived
// from cfg.Region ("eu" -> EU endpoint, anything else -> US) unless overridden
// by WithMailgunBaseURL. The transport starts healthy (optimistic); its health
// is updated after each Send per the failure classification.
func NewMailgunTransport(cfg MailgunConfig, opts ...MailgunOption) *MailgunTransport {
	t := &MailgunTransport{
		cfg:     cfg,
		client:  &http.Client{Timeout: mailgunTimeout},
		baseURL: baseURLForRegion(cfg.Region),
		healthy: true,
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// baseURLForRegion maps a region string to its Mailgun API base URL. Matching
// is case-insensitive; only "eu" selects the EU endpoint, everything else
// (including the empty default) uses US.
func baseURLForRegion(region string) string {
	if strings.EqualFold(strings.TrimSpace(region), "eu") {
		return mailgunBaseURLEU
	}
	return mailgunBaseURLUS
}

// LastMessageID returns the Mailgun message-id captured from the most recent
// successful send (for audit/test). Empty before the first success.
func (t *MailgunTransport) LastMessageID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastMsgID
}

// LastStatus returns the HTTP status code of the most recent send attempt (0
// before the first attempt, or when the request never got a response).
func (t *MailgunTransport) LastStatus() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastStatus
}

// Healthy reports whether the last send attempt left the provider looking
// usable: true after a 2xx (or a permanent per-message 4xx, which reflects a
// bad recipient rather than a broken provider), false after an auth failure or
// a transient failure (429/5xx/dial). Slice C's circuit-breaker consumes this
// signal; the breaker itself is out of scope here.
func (t *MailgunTransport) Healthy() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.healthy
}

// Send implements email.Transport. It builds a multipart/form-data body from
// the Message, POSTs it to Mailgun's messages API with HTTP Basic auth
// (api:<key>), and classifies the response:
//
// - 2xx -> success; the returned message-id is captured.
// - 401/403 -> ErrMailgunAuth (non-transient): a config fault, retrying
// the same request is pointless.
// - 429 / 5xx -> email.TransientError: go-email's Retry gives it a couple
// of tries; the Slice-C breaker opens if it persists.
// - dial/timeout -> email.TransientError (no HTTP response at all).
// - other 4xx -> ordinary error (permanent per-message, e.g. a bad
// recipient): fail this one send without marking the whole provider down.
func (t *MailgunTransport) Send(ctx context.Context, msg email.Message) error {
	logger := logctx.From(ctx)

	body, contentType, err := buildMailgunForm(msg, t.cfg.FromAddress)
	if err != nil {
		return fmt.Errorf("mailgun: build request body: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v3/%s/messages", t.baseURL, t.cfg.Domain)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mailgun: build request: %w", err)
	}
	req.SetBasicAuth("api", t.cfg.APIKey)
	req.Header.Set("Content-Type", contentType)

	resp, err := t.client.Do(req)
	if err != nil {
		// Dial/timeout/connection reset: no HTTP response at all -> transient.
		t.record(0, false)
		logger.Warn().Err(err).Str("mailgun_domain", t.cfg.Domain).Msg("mailgun send transport error")
		return email.TransientError{Err: fmt.Errorf("mailgun: transport: %w", err)}
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		var parsed struct {
			ID string `json:"id"`
		}
		// A malformed success body is not fatal: the send succeeded, we just
		// lose the message-id. Read a bounded amount to avoid a runaway body.
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&parsed)
		t.recordSuccess(resp.StatusCode, parsed.ID)
		logger.Debug().Int("status", resp.StatusCode).Str("mailgun_message_id", parsed.ID).Msg("mailgun send accepted")
		return nil

	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		t.record(resp.StatusCode, false)
		logger.Error().Int("status", resp.StatusCode).Msg("mailgun auth rejected")
		return fmt.Errorf("mailgun: status %d: %w", resp.StatusCode, ErrMailgunAuth)

	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		t.record(resp.StatusCode, false)
		logger.Warn().Int("status", resp.StatusCode).Msg("mailgun transient failure")
		return email.TransientError{Err: fmt.Errorf("mailgun: status %d: transient", resp.StatusCode)}

	default: // other 4xx -> permanent per-message (e.g. bad recipient)
		// The provider is fine; keep it healthy so one bad address does not
		// pause the whole platform.
		t.record(resp.StatusCode, true)
		logger.Warn().Int("status", resp.StatusCode).Msg("mailgun permanent per-message failure")
		return fmt.Errorf("mailgun: status %d: permanent", resp.StatusCode)
	}
}

// recordSuccess stores the message-id and marks the transport healthy.
func (t *MailgunTransport) recordSuccess(status int, msgID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastStatus = status
	t.lastMsgID = msgID
	t.healthy = true
}

// record updates the last status and health without touching the message-id.
func (t *MailgunTransport) record(status int, healthy bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastStatus = status
	t.healthy = healthy
}

// buildMailgunForm renders msg into a multipart/form-data body for Mailgun's
// messages API and returns the body plus its Content-Type (which carries the
// multipart boundary). from falls back to fromDefault when the Message carries
// no From. Attachments become file parts (regular under "attachment", inline
// under "inline"); everything else is a plain form field.
func buildMailgunForm(msg email.Message, fromDefault string) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	from := msg.From
	if from == "" {
		from = fromDefault
	}

	fields := func(field string, values ...string) error {
		for _, v := range values {
			if err := w.WriteField(field, v); err != nil {
				return err
			}
		}
		return nil
	}

	if err := fields("from", from); err != nil {
		return nil, "", err
	}
	if err := fields("to", msg.To...); err != nil {
		return nil, "", err
	}
	if err := fields("cc", msg.Cc...); err != nil {
		return nil, "", err
	}
	if err := fields("bcc", msg.Bcc...); err != nil {
		return nil, "", err
	}
	if err := w.WriteField("subject", msg.Subject); err != nil {
		return nil, "", err
	}
	if msg.HTML != "" {
		if err := w.WriteField("html", msg.HTML); err != nil {
			return nil, "", err
		}
	}
	if msg.Text != "" {
		if err := w.WriteField("text", msg.Text); err != nil {
			return nil, "", err
		}
	}
	if msg.ReplyTo != "" {
		if err := w.WriteField("h:Reply-To", msg.ReplyTo); err != nil {
			return nil, "", err
		}
	}
	// Mailgun forwards arbitrary custom headers via "h:<Name>" form fields; the
	// SMTP transport renders msg.ListUnsubscribe/ListUnsubscribePost natively,
	// but the Mailgun API leg would otherwise drop them, so the RFC 8058
	// one-click List-Unsubscribe headers must be
	// emitted explicitly here to survive prod's Mailgun path.
	if msg.ListUnsubscribe != "" {
		if err := w.WriteField("h:List-Unsubscribe", msg.ListUnsubscribe); err != nil {
			return nil, "", err
		}
	}
	if msg.ListUnsubscribePost != "" {
		if err := w.WriteField("h:List-Unsubscribe-Post", msg.ListUnsubscribePost); err != nil {
			return nil, "", err
		}
	}
	// Any other explicit custom headers (e.g. the dev catch-all marker) also go
	// out as h:<Name> fields so the Mailgun path matches the SMTP path.
	for k, v := range msg.Headers {
		if v == "" {
			continue
		}
		if err := w.WriteField("h:"+k, v); err != nil {
			return nil, "", err
		}
	}

	for _, att := range msg.Attachments {
		if err := writeAttachment(w, att); err != nil {
			return nil, "", err
		}
	}

	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// writeAttachment writes a single attachment as a Mailgun file part. Inline
// attachments use the "inline" field and are referenced from HTML as
// cid:<name>; regular attachments use "attachment". The part filename defaults
// to the ContentID for an inline part without an explicit filename so the
// cid reference resolves.
func writeAttachment(w *multipart.Writer, att email.Attachment) error {
	field := "attachment"
	name := att.Filename
	if att.Inline {
		field = "inline"
		if name == "" {
			name = att.ContentID
		}
	}

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name=%q; filename=%q`, field, name))
	ct := att.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)

	part, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = part.Write(att.Content)
	return err
}

// interface guard: MailgunTransport must satisfy go-email's Transport.
var _ email.Transport = (*MailgunTransport)(nil)
