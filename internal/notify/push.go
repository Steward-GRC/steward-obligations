// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// FCMTokenResolver maps a user_id to the FCM device token registered for
// that user; the Identity service owns this mapping in production.
type FCMTokenResolver interface {
	ResolveFCMToken(ctx context.Context, userID string) (string, error)
}

// defaultEndpointTemplate is the Firebase Cloud Messaging v1 HTTP endpoint
// with a single %s placeholder for the project id. Held as a constant so
// tests can swap it out via WithEndpoint and still keep the format-string
// contract identical.
const defaultEndpointTemplate = "https://fcm.googleapis.com/v1/projects/%s/messages:send"

// PushChannel delivers mobile push notifications via the Firebase Cloud
// Messaging v1 HTTP API. We avoid the Firebase Admin SDK (firebase.google.com/go)
// here so this package stays free of the heavy gRPC + IAM transitive
// dependency tree; production wiring attaches an OAuth2-aware
// http.Client (Application Default Credentials) via WithHTTPClient so the
// Authorization header is automatic.
type PushChannel struct {
	projectID        string
	endpointTemplate string
	resolver         FCMTokenResolver
	client           *http.Client
}

// NewPushChannel returns a PushChannel using the default FCM endpoint and a
// vanilla http.Client. Production wiring should call WithHTTPClient to inject
// a client that adds the Bearer token via Application Default Credentials.
func NewPushChannel(projectID string, r FCMTokenResolver) *PushChannel {
	return &PushChannel{
		projectID:        projectID,
		endpointTemplate: defaultEndpointTemplate,
		resolver:         r,
		client:           &http.Client{},
	}
}

// WithHTTPClient swaps in a custom http.Client; production uses one that
// injects the OAuth2 Authorization header from Application Default
// Credentials, tests use one wired to httptest.NewServer.
func (p *PushChannel) WithHTTPClient(c *http.Client) *PushChannel {
	p.client = c
	return p
}

// WithEndpoint overrides the endpoint template. The template must contain
// a single %s placeholder for the project id. Tests use this to redirect
// FCM calls at an httptest.NewServer URL.
func (p *PushChannel) WithEndpoint(template string) *PushChannel {
	p.endpointTemplate = template
	return p
}

// Send resolves the FCM token for the recipient and POSTs an FCM v1 message
// payload. Empty tokens (user has not registered a device) are not an error;
// we silently skip so the dispatcher can keep fanning to other channels.
func (p *PushChannel) Send(ctx context.Context, payload AckReminderPayload) error {
	// Push needs a messaging project per adopter, so it is off until one is set.
	if p.projectID == "" {
		return nil
	}
	token, err := p.resolver.ResolveFCMToken(ctx, payload.UserID)
	if err != nil {
		return err
	}
	if token == "" {
		return nil
	}

	body, err := json.Marshal(map[string]any{
		"message": map[string]any{
			"token": token,
			"notification": map[string]string{
				"title": "Policy Acknowledgment Required",
				"body":  fmt.Sprintf("Please acknowledge policy version %s.", payload.PolicyVersionID),
			},
			"data": map[string]string{
				"policy_version_id": payload.PolicyVersionID,
				"campaign_id":       payload.CampaignID,
			},
		},
	})
	if err != nil {
		return err
	}

	url := fmt.Sprintf(p.endpointTemplate, p.projectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("FCM returned HTTP %d", resp.StatusCode)
	}
	return nil
}
