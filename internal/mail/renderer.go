// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package mail implements go-email's Renderer interface by delegating to the
// branded-HTML render sidecar (see render-sidecar/) over localhost HTTP.
package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"

	email "github.com/Bugs5382/go-email"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// defaultTimeout bounds how long the sidecar has to respond. The sidecar is
// localhost-only, so this is headroom against a wedged process, not a
// network-latency budget.
const defaultTimeout = 10 * time.Second

// tracer emits a span per sidecar render call onto the global TracerProvider
// wired by the binary's gootel.Init call.
var tracer = otel.Tracer("github.com/Steward-GRC/steward-obligations/internal/mail")

// renderRequest is the wire payload posted to the sidecar's /render endpoint.
type renderRequest struct {
	Kind string `json:"kind"`
	Vars any    `json:"vars"`
}

// renderResponse is the wire payload the sidecar returns on success.
type renderResponse struct {
	Subject string `json:"subject"`
	HTML    string `json:"html"`
}

// SidecarRenderer implements go-email's Renderer by delegating rendering to
// the render sidecar. There is no plaintext fallback: a sidecar failure or
// malformed response surfaces as an error rather than shipping an unbranded
// or empty email.
type SidecarRenderer struct {
	baseURL string
	hc      *http.Client
}

// NewSidecarRenderer builds a SidecarRenderer that POSTs to
// "${baseURL}/render". A nil hc gets a default client with a bounded
// timeout so a wedged sidecar can't hang the send path indefinitely.
func NewSidecarRenderer(baseURL string, hc *http.Client) *SidecarRenderer {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	return &SidecarRenderer{baseURL: baseURL, hc: hc}
}

// Render implements email.Renderer. It POSTs {"kind": kind, "vars": data} to
// the sidecar and maps the {"subject", "html"} response onto an
// email.Rendered. Text is left empty -- the sidecar only renders HTML.
func (r *SidecarRenderer) Render(ctx context.Context, kind string, data any) (email.Rendered, error) {
	ctx, span := tracer.Start(ctx, "mail.SidecarRenderer.Render")
	defer span.End()
	span.SetAttributes(attribute.String("email.render.kind", kind))

	logger := logctx.From(ctx)

	body, err := json.Marshal(renderRequest{Kind: kind, Vars: data})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "marshal render request")
		return email.Rendered{}, fmt.Errorf("mail: marshal render request for kind %q: %w", kind, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/render", bytes.NewReader(body))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "build render request")
		return email.Rendered{}, fmt.Errorf("mail: build render request for kind %q: %w", kind, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.hc.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "call render sidecar")
		logger.Error().Err(err).Str("kind", kind).Msg("render sidecar call failed")
		return email.Rendered{}, fmt.Errorf("mail: call render sidecar for kind %q: %w", kind, err)
	}
	defer func() { _ = resp.Body.Close() }()

	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		span.SetStatus(codes.Error, "render sidecar non-200")
		logger.Error().Int("status", resp.StatusCode).Str("kind", kind).Msg("render sidecar returned non-200")
		return email.Rendered{}, fmt.Errorf("mail: render sidecar returned %d for kind %q: %s", resp.StatusCode, kind, bytes.TrimSpace(respBody))
	}

	var out renderResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "decode render response")
		return email.Rendered{}, fmt.Errorf("mail: decode render response for kind %q: %w", kind, err)
	}

	logger.Debug().Str("kind", kind).Msg("render sidecar succeeded")
	return email.Rendered{Subject: out.Subject, HTML: out.HTML}, nil
}
