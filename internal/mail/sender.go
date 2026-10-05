// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	email "github.com/Bugs5382/go-email"
	emailotel "github.com/Bugs5382/go-email/otel"
	"github.com/Bugs5382/go-email/smtp"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"

	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
	"github.com/Steward-GRC/steward-obligations/internal/notifytoken"
)

// defaultFrom is the sender used when SenderConfig.From is empty. Deployments
// set SMTP_FROM; this placeholder only keeps an unset value valid.
const defaultFrom = "no-reply@example.org"

// devCatchAllHeader records a message's real, pre-redirect recipients when
// devCatchAll has rewritten them for a dev-environment send, so a developer
// inspecting maildev can still see who the mail was really addressed to.
const devCatchAllHeader = "X-Dev-Original-Recipients"

// senderInstrumentationName identifies this file's tracer and meter,
// distinct from renderer.go's own tracer name.
const senderInstrumentationName = "github.com/Steward-GRC/steward-obligations/internal/mail.Sender"

// SenderConfig holds the values NewSender needs to build a branded go-email
// Sender: SMTP relay coordinates, the render sidecar's base URL, and the
// dev catch-all redirect target. Callers source these from the service's
// internal/config.Config (SMTPHost/SMTPPort/SMTPUsername/SMTPFrom/
// SMTPPassword/SMTPStartTLS and the shared platform APP_ENV) plus the
// sidecar's listen address.
type SenderConfig struct {
	SMTPHost string
	SMTPPort string
	// SMTPUsername is the SMTP AUTH identity for a real relay (e.g. an
	// SES/SendGrid API key or service-account username), which is often not
	// the same address as From. Only consulted when Password is set; see
	// BuildSMTPConfig.
	SMTPUsername string
	From         string
	Password     string
	SidecarURL   string
	AppEnv       string
	// SMTPStartTLS requests STARTTLS against a real relay. It is ignored
	// (forced off) when AppEnv=="dev", since dev always targets a local,
	// unauthenticated maildev catcher with no TLS support.
	SMTPStartTLS bool
	// SMTPTLSInsecure skips STARTTLS certificate verification. The internal
	// relay presents a legacy CN-only cert (no SANs) that Go rejects on verify;
	// true keeps the connection encrypted but unverified. Only consulted when
	// TLS is on (i.e. AppEnv != "dev").
	SMTPTLSInsecure bool
	DevCatchAllTo   string
	// LogoSrc is the absolute, PUBLIC HTTPS URL of the brand logo stamped into
	// every email's `logoSrc` template var so all templates render the same
	// logo. Must be publicly reachable — mail clients fetch it externally.
	LogoSrc string
	// ProductName and LegalText are adopter branding: the product name in the
	// header and the legal or postal line in the footer. Empty leaves the
	// render sidecar's defaults (the product name "Steward", no footer line).
	ProductName string
	LegalText   string
}

// senderOptions collects the values an Option can override in NewSender.
type senderOptions struct {
	transport    email.Transport
	recorder     email.Recorder
	deduper      email.Deduper
	prefs        PrefSource
	quiet        QuietHoursSource
	resolver     *notifpolicy.Resolver
	pauseDecider PauseDecider
	pauseOutbox  OutboxWriter
	unsub        *unsubLinks
}

// unsubLinks carries everything Send needs to stamp the manage/unsubscribe
// links: the signer that mints the tokens, the
// public one-click endpoint base URL, the preferences-page base URL, and the
// token TTL. It is nil unless WithUnsubscribeLinks is supplied, so a Sender
// wired without it behaves exactly as before (no headers, no link stamping).
type unsubLinks struct {
	signer   *notifytoken.Signer
	unsubURL string // full endpoint, e.g. https://policy.example.org/notify/unsubscribe
	prefsURL string // preferences page base, e.g. https://policy.example.org
	ttl      time.Duration
}

// Option configures a Sender built by NewSender. These are a seam: tests use
// WithTransport to substitute a fake transport instead of dialing real SMTP,
// WithRecorder wires the service's concrete audit emitter into the
// middleware chain's Record hook, WithDeduper overrides the default
// in-memory idempotency cache, and WithSuppressor wires the prefs/quiet-hours
// backstop.
type Option func(*senderOptions)

// WithTransport overrides the go-email Transport NewSender would otherwise
// dial from cfg via smtp.NewSMTPTransport.
func WithTransport(t email.Transport) Option {
	return func(o *senderOptions) { o.transport = t }
}

// WithRecorder overrides the go-email Recorder wired into the middleware
// chain as the audit hook. Without it, sends are still delivered but no
// audit event is emitted (the chain uses email.NopRecorder{}).
func WithRecorder(r email.Recorder) Option {
	return func(o *senderOptions) { o.recorder = r }
}

// WithDeduper overrides the Deduper wired into the middleware chain's Dedupe
// hook. Without it, NewSender defaults to a process-local TTLDeduper (see
// deduper.go) so idempotency works out of the box with no external wiring;
// callers that need durable, multi-replica dedup (a later hardening pass)
// supply their own.
func WithDeduper(d email.Deduper) Option {
	return func(o *senderOptions) { o.deduper = d }
}

// WithPauseGate installs the Phase-7 pause-gate as the OUTERMOST middleware
// (before devCatchAll): before any other hook runs, decider decides whether a
// working transport exists, and if not the fully-rendered message is held in
// outbox and the send returns ErrMailPaused (see PauseGate). Without this
// option NewSender builds no pause-gate, so every send flows straight to the
// transport exactly as before — the whole feature is opt-in at wire time.
func WithPauseGate(decider PauseDecider, outbox OutboxWriter) Option {
	return func(o *senderOptions) { o.pauseDecider = decider; o.pauseOutbox = outbox }
}

// WithSuppressor wires the prefs/quiet-hours sources the Suppress hook uses
// to decide whether a send goes out (see suppressor.go). Without it, prefs
// and quiet-hours are both nil and the Suppress hook is a no-op passthrough
// -- every send goes out. cmd/server wires the service's real NotifPref
// store adapter and internal/notify's shared QuietHours here.
func WithSuppressor(prefs PrefSource, quiet QuietHoursSource) Option {
	return func(o *senderOptions) { o.prefs = prefs; o.quiet = quiet }
}

// WithResolver wires the shared notifpolicy.Resolver as the Suppress hook's
// decision point. When set it supersedes WithSuppressor:
// the resolver folds the channel switches, quiet hours (bypassed for mandatory
// security/transactional and critical severity), and the per-category cadence
// (off suppresses an optional type; the compliance floor keeps mandatory mail
// sending) into one decision, so this backstop and internal/notify's Dispatcher
// enforce the identical policy at send time.
func WithResolver(r *notifpolicy.Resolver) Option {
	return func(o *senderOptions) { o.resolver = r }
}

// WithUnsubscribeLinks wires the signed manage/unsubscribe links
// . When set, every Send:
//
// - stamps a signed, tokenized "manage email preferences" deep-link
// (preferencesUrl var) into the branded footer of ALL mail; and
// - on OPTIONAL sends only (workflow / informational — never a mandatory
// compliance / security / transactional kind), adds RFC 8058 one-click
// List-Unsubscribe + List-Unsubscribe-Post headers (and an unsubscribeHref
// footer var) pointing at a tokenized unsubscribe endpoint that maps to
// "set this category OFF".
//
// signer mints the HMAC tokens; unsubURL is the public one-click endpoint the
// header targets (the gateway's unauthenticated /notify/unsubscribe handler);
// prefsURL is the preferences-page base the footer link resolves to; ttl bounds
// how long a link stays valid. Without this option the Sender adds no headers
// and stamps no links — the whole feature is opt-in at wire time.
func WithUnsubscribeLinks(signer *notifytoken.Signer, unsubURL, prefsURL string, ttl time.Duration) Option {
	return func(o *senderOptions) {
		if signer == nil {
			return
		}
		o.unsub = &unsubLinks{signer: signer, unsubURL: unsubURL, prefsURL: prefsURL, ttl: ttl}
	}
}

// Sender sends branded, kind-templated emails through go-email: kind+vars
// are resolved to subject/HTML by the SidecarRenderer (see renderer.go), and
// every send runs through the standard middleware chain (devCatchAll ->
// Record -> Validate -> Retry -> otel) before reaching the SMTP transport.
type Sender struct {
	sender   email.Sender
	from     string
	branding map[string]string
	unsub    *unsubLinks
}

// NewSender builds a Sender backed by:
// - a go-email SMTP transport dialed from cfg via BuildSMTPConfig: SMTP
// AUTH is attempted only when cfg.Password is set (a real relay), so a
// bare/unauthenticated maildev catcher (no password configured) is never
// sent an AUTH command it would reject; STARTTLS is requested per
// cfg.SMTPStartTLS everywhere except cfg.AppEnv=="dev", which always
// targets a local, TLS-less maildev catcher regardless of that setting
// -- or the transport supplied via WithTransport;
// - a SidecarRenderer pointed at cfg.SidecarURL;
// - the middleware chain, outermost to innermost: devCatchAll, Dedupe,
// Suppress, Record (audit), Validate, Retry(3, time.Second), and the
// go-email/otel Middleware.
//
// The chain order encodes the compliance rules:
// - Dedupe is outermost of the compliance hooks so a redelivered/duplicate
// trigger for an already-sent (userID, kind, dedupRef) is caught before
// any prefs lookup or audit write runs at all -- the cheapest check
// gates the more expensive ones. go-email's Dedupe only Marks a key
// after next succeeds, so a message this chain goes on to suppress is
// never marked as sent (see below), and a later, unsuppressed retry of
// the same key still goes out.
// - Suppress sits inside Dedupe but outside Record: it returns
// email.ErrSuppressed (a skip, not an error) when the recipient's email
// preference is off or they are in quiet hours, except kind
// "welcome-account" which always bypasses both checks (see
// suppressor.go). Because Record is nested inside Suppress, a suppressed
// send never reaches Record -- it is neither delivered nor audited as a
// send, and (per the point above) never marked seen by Dedupe either.
// - Record sits outside Retry so one logical send produces exactly one
// audit event carrying the final outcome (sent or failed), not one per
// delivery attempt; without WithRecorder it defaults to
// email.NopRecorder{} (no audit event, but sends still deliver).
// - otel sits inside Retry so it records a span/metric per delivery
// attempt.
//
// When cfg.AppEnv=="dev", devCatchAll rewrites every outgoing message's
// To/Cc/Bcc to cfg.DevCatchAllTo and stamps the real recipients into the
// X-Dev-Original-Recipients header.
// BuildSMTPConfig translates cfg (plus the already-defaulted from address)
// into the go-email smtp.Config that NewSender dials via
// smtp.NewSMTPTransport. It is exported as a seam so tests can assert on the
// resulting auth/TLS posture directly rather than standing up a real SMTP
// listener; NewSender itself calls this to build the config for its default
// transport.
//
// - User (and therefore SMTP AUTH) is only set when cfg.Password is
// configured -- go-email's SMTPTransport attempts AUTH whenever User is
// non-empty, regardless of TLS/AppEnv, so a target with no password (an
// unauthenticated dev/qa maildev catcher) must get an empty User to reach
// go-email's plaintext, no-auth send path. When a password is set,
// cfg.SMTPUsername is the AUTH identity (a real relay's auth username is
// frequently not the envelope From address, e.g. an SES/SendGrid API
// key); if SMTPUsername is unset, From is used, preserving prior
// behavior for relays that authenticate as their From address.
// - TLS is requested per cfg.SMTPStartTLS, but forced off when
// cfg.AppEnv=="dev", since dev always targets a local maildev catcher
// with no STARTTLS support, independent of how SMTPStartTLS is
// configured.
func BuildSMTPConfig(cfg SenderConfig, from string) (smtp.Config, error) {
	port, err := strconv.Atoi(cfg.SMTPPort)
	if err != nil {
		return smtp.Config{}, fmt.Errorf("mail: invalid SMTP port %q: %w", cfg.SMTPPort, err)
	}

	// Authenticate only against a real relay (password configured). In dev/test
	// the SMTP endpoint is an unauthenticated maildev with no password, so User
	// stays empty and go-email skips SMTP AUTH.
	user := ""
	if cfg.Password != "" {
		user = cfg.SMTPUsername
		if user == "" {
			user = from // preserve prior behavior: authenticate as From when no explicit username is set
		}
	}

	return smtp.Config{
		Host: cfg.SMTPHost,
		Port: port,
		User: user,
		Pass: cfg.Password,
		From: from,
		// STARTTLS against a real relay per SMTPStartTLS, forced off in dev
		// (a local, TLS-less maildev catcher) regardless of that setting.
		TLS: cfg.SMTPStartTLS && cfg.AppEnv != "dev",
		// Skip cert verification for the internal relay's legacy CN-only cert
		// (still encrypted). Only takes effect when TLS is on.
		TLSInsecure: cfg.SMTPTLSInsecure,
	}, nil
}

// BuildDefaultSMTPTransport builds the SMTP transport NewSender would dial by
// default (same From defaulting and BuildSMTPConfig posture), returned as a
// bare email.Transport. It exists so a caller wiring a RoutingTransport can
// supply the SMTP leg as the fallback while still routing through NewSender via
// WithTransport — keeping the SMTP auth/TLS logic in one place.
func BuildDefaultSMTPTransport(cfg SenderConfig) (email.Transport, error) {
	from := cfg.From
	if from == "" {
		from = defaultFrom
	}
	smtpCfg, err := BuildSMTPConfig(cfg, from)
	if err != nil {
		return nil, err
	}
	return smtp.NewSMTPTransport(smtpCfg), nil
}

func NewSender(cfg SenderConfig, opts ...Option) (*Sender, error) {
	o := &senderOptions{}
	for _, opt := range opts {
		opt(o)
	}

	from := cfg.From
	if from == "" {
		from = defaultFrom
	}

	transport := o.transport
	if transport == nil {
		smtpCfg, err := BuildSMTPConfig(cfg, from)
		if err != nil {
			return nil, err
		}
		transport = smtp.NewSMTPTransport(smtpCfg)
	}

	recorder := o.recorder
	if recorder == nil {
		recorder = email.NopRecorder{}
	}

	deduper := o.deduper
	if deduper == nil {
		deduper = NewTTLDeduper(DefaultDedupWindow)
	}

	tracer := otel.Tracer(senderInstrumentationName)
	meter := otel.Meter(senderInstrumentationName)

	// The pause-gate, when wired, is prepended as the OUTERMOST middleware so it
	// runs before devCatchAll and every compliance hook: a held send is never
	// audited, deduped-as-seen, or delivered. Without WithPauseGate the chain is
	// exactly the pre-Phase-7 chain.
	// Prefer the shared PrefResolver when wired (channel switches + quiet hours
	// + category cadence); otherwise fall back to the channel-pref + quiet-hours
	// suppressor. Both return email.ErrSuppressed for a blocked send.
	suppress := suppressByPrefsAndQuietHours(o.prefs, o.quiet)
	if o.resolver != nil {
		suppress = suppressByResolver(o.resolver)
	}

	mws := []email.Middleware{
		devCatchAll(cfg.AppEnv, cfg.DevCatchAllTo),
		email.Dedupe(deduper),
		suppress,
		email.Record(recorder),
		email.Validate(),
		email.Retry(3, time.Second),
		emailotel.Middleware(tracer, meter),
	}
	if o.pauseDecider != nil {
		mws = append([]email.Middleware{PauseGate(o.pauseDecider, o.pauseOutbox)}, mws...)
	}

	sender := email.New(transport,
		email.WithRenderer(NewSidecarRenderer(cfg.SidecarURL, nil)),
		email.WithMiddleware(mws...),
	)

	branding := map[string]string{}
	for k, v := range map[string]string{"logoSrc": cfg.LogoSrc, "productName": cfg.ProductName, "legalText": cfg.LegalText} {
		if v != "" {
			branding[k] = v
		}
	}
	return &Sender{sender: sender, from: from, branding: branding, unsub: o.unsub}, nil
}

// SendRendered replays an already-rendered, held OutboxItem back through the
// full middleware chain WITHOUT re-rendering (the bodies are the ones captured
// at hold time). The drainer calls it on recovery. The message is stamped as an
// outbox replay so the pause-gate, if the breaker has reopened mid-drain,
// holds it again WITHOUT persisting a duplicate row. Dedupe still runs, and the
// durable outbox (row status) is the real replay-once guard so a redelivery
// never double-sends.
func (s *Sender) SendRendered(ctx context.Context, it OutboxItem) error {
	from := it.From
	if from == "" {
		from = s.from
	}
	msg := email.Message{
		From:         from,
		EnvelopeFrom: from,
		To:           splitRecipients(it.Recipient),
		Subject:      it.Subject,
		HTML:         it.HTML,
		Text:         it.Text,
		Meta: map[string]any{
			"kind":              it.Kind,
			"user_id":           it.UserID,
			"message_id":        uuid.NewString(),
			outboxReplayMetaKey: true,
		},
	}
	if it.DedupKey != "" {
		msg.Meta["dedup_key"] = it.DedupKey
	}
	if err := s.sender.Send(ctx, msg); err != nil {
		return fmt.Errorf("mail: replay kind %q to %q: %w", it.Kind, it.Recipient, err)
	}
	return nil
}

// splitRecipients reverses outboxItemFromMessage's join: a comma-separated
// recipient string back into a To slice. A single address (the common case) is
// returned as a one-element slice.
func splitRecipients(recipient string) []string {
	if recipient == "" {
		return nil
	}
	parts := strings.Split(recipient, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Send resolves kind+vars via the SidecarRenderer into a subject/HTML body
// and delivers it to to, running the message through the full middleware
// chain (see NewSender for the fixed order).
//
// userID identifies who this send is for: the Suppress hook uses it to look
// up the recipient's email preference and quiet-hours window (bypassed
// entirely for kind "welcome-account"), and the Record hook stamps it onto
// the audit event as the subject user. An empty userID skips the
// prefs/quiet-hours check outright -- there's no one to look a preference up
// for -- so it always sends.
//
// dedupRef is the caller's business key for this notification (e.g. a
// policy version or campaign id); combined with userID and kind it forms the
// Deduper's idempotency key, so a redelivered/duplicate trigger for the same
// (userID, kind, dedupRef) within the dedup window is skipped rather than
// double-sent. An empty userID or dedupRef disables deduplication for this
// send (every call goes through).
func (s *Sender) Send(ctx context.Context, kind, userID, to, dedupRef string, vars any) error {
	msg := email.Message{
		From:         s.from,
		EnvelopeFrom: s.from,
		To:           []string{to},
		Meta: map[string]any{
			"kind":       kind,
			"user_id":    userID,
			"message_id": uuid.NewString(),
		},
	}
	if userID != "" && dedupRef != "" {
		msg.Meta["dedup_key"] = strings.Join([]string{userID, kind, dedupRef}, ":")
	}
	// Stamp the adopter branding into every template's vars, unless the caller
	// set a value of its own. All callers pass map vars.
	if m, ok := vars.(map[string]any); ok {
		for k, v := range s.branding {
			if _, set := m[k]; !set {
				m[k] = v
			}
		}
	}
	// Stamp the signed manage/unsubscribe links:
	// a preferences deep-link in the footer of ALL mail, and RFC 8058 one-click
	// List-Unsubscribe headers on OPTIONAL sends only. See applyUnsubscribeLinks.
	s.applyUnsubscribeLinks(&msg, kind, userID, vars)
	if err := s.sender.SendKind(ctx, kind, msg, vars); err != nil {
		return fmt.Errorf("mail: send kind %q to %q: %w", kind, to, err)
	}
	return nil
}

// applyUnsubscribeLinks stamps the signed manage/unsubscribe affordances onto a
// send, a no-op unless WithUnsubscribeLinks
// was supplied and the send is addressed to a known user:
//
// - preferencesUrl (footer "manage email preferences" link) is stamped on
// ALL mail. It OVERRIDES any caller-supplied value so the link is always the
// signed, per-recipient one rather than a bare static URL.
// - List-Unsubscribe + List-Unsubscribe-Post (one-click, RFC 8058) headers,
// and an unsubscribeHref footer var, are stamped ONLY when the kind
// classifies as OPTIONAL (workflow / informational). Mandatory kinds
// (compliance / security / transactional) and unknown kinds get NO
// unsubscribe affordance — a compliance floor an unsubscribe could never
// satisfy, and (for unknown kinds) a category we cannot safely name.
//
// A token-mint failure is swallowed (the send still goes out, just without the
// link) rather than blocking mail on a signing hiccup.
func (s *Sender) applyUnsubscribeLinks(msg *email.Message, kind, userID string, vars any) {
	if s.unsub == nil || userID == "" {
		return
	}
	m, ok := vars.(map[string]any)
	if !ok {
		return
	}

	if prefsTok, err := s.unsub.signer.MintPreferences(userID, s.unsub.ttl); err == nil {
		m["preferencesUrl"] = appendToken(s.unsub.prefsURL, prefsTok)
	}

	class, known := notifpolicy.Classify(kind)
	if !known || class.Mandatory() {
		return
	}
	unsubTok, err := s.unsub.signer.MintUnsubscribe(userID, string(class.Category), s.unsub.ttl)
	if err != nil {
		return
	}
	href := appendToken(s.unsub.unsubURL, unsubTok)
	// RFC 2369 requires the URL be angle-bracketed; RFC 8058 one-click pairs it
	// with the fixed List-Unsubscribe-Post value.
	msg.ListUnsubscribe = "<" + href + ">"
	msg.ListUnsubscribePost = "List-Unsubscribe=One-Click"
	m["unsubscribeHref"] = href
}

// appendToken appends a token query parameter to base, choosing "?" or "&"
// depending on whether base already carries a query string.
func appendToken(base, token string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "token=" + url.QueryEscape(token)
}

// devCatchAll returns a middleware that, when appEnv=="dev", redirects every
// message's To/Cc/Bcc to catchAllTo, stamping the original recipients into
// devCatchAllHeader for debugging. Outside dev, or with no catchAllTo
// configured, it is a no-op passthrough.
func devCatchAll(appEnv, catchAllTo string) email.Middleware {
	return func(next email.SendFunc) email.SendFunc {
		return func(ctx context.Context, m *email.Message) error {
			if appEnv != "dev" || catchAllTo == "" {
				return next(ctx, m)
			}

			original := m.Recipients()
			if len(original) > 0 {
				if m.Headers == nil {
					m.Headers = map[string]string{}
				}
				m.Headers[devCatchAllHeader] = strings.Join(original, ", ")
				m.To = []string{catchAllTo}
				m.Cc = nil
				m.Bcc = nil
			}

			return next(ctx, m)
		}
	}
}
