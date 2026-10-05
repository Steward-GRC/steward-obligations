// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package notifytoken mints and verifies the signed, stateless tokens that back
// the email manage/unsubscribe links and the
// email-address verification link.
//
// Three link kinds carry a token:
//
// - a one-click List-Unsubscribe link (RFC 8058) on OPTIONAL sends, whose
// token names the recipient AND the taxonomy category to silence
// (PurposeUnsubscribe);
// - a "manage email preferences" deep-link in the footer of ALL mail, whose
// token names only the recipient (PurposePreferences); and
// - a "verify your email address" CTA link on the welcome/account-creation
// flow, whose token names the recipient AND the exact address being
// proven (PurposeEmailVerify) — see 's port of the Sneakers
// mailbox-proof flow onto the obligations service's own stateless-token
// convention (Sneakers itself has no signed-link equivalent: it proves
// mailbox control with a DB-hashed, single-use numeric OTP for MFA/reset,
// a different mechanism entirely; this purpose reuses the obligations service's
// OWN existing link convention rather than porting that OTP model).
//
// The token is a self-contained HMAC-SHA256 MAC over a compact JSON payload —
// the standard stateless signed-token construction, NOT a bespoke crypto
// scheme: no server-side token store, no new dependency. the obligations service is
// the sole holder of the signing secret; it mints tokens at send time and the
// public edge (the gateway's unauthenticated /notify/unsubscribe handler, and —
// for PurposeEmailVerify — an equivalent public verify-email handler) verifies
// them with the same secret before mapping an unsubscribe to "set that category
// OFF" via the NotifPrefService (the compliance floor still rejects OFF for
// mandatory categories server-side), or an email verification to "mark this
// address verified" via the Identity service.
//
// Wire format: base64url(payloadJSON) + "." + base64url(HMAC-SHA256(secret, base64url(payloadJSON)))
// The MAC is computed over the base64url payload segment (not the raw JSON) so
// verification never has to re-serialize JSON to reproduce the signed bytes.
package notifytoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Purpose distinguishes what a token authorizes so an unsubscribe token can
// never be replayed as a preferences token or vice-versa.
type Purpose string

const (
	// PurposeUnsubscribe authorizes silencing one category for one user
	// (the one-click List-Unsubscribe endpoint). Carries UserID + Category.
	PurposeUnsubscribe Purpose = "unsub"
	// PurposePreferences authorizes landing a user on their preferences page
	// (the footer "manage email preferences" link). Carries UserID only.
	PurposePreferences Purpose = "prefs"
	// PurposeEmailVerify authorizes marking one specific address verified for
	// one specific user (the "verify your email" CTA link, ). Carries
	// UserID + Email — binding the token to the exact address it was minted
	// for means a later address change can never be waved through by an
	// already-issued token for the old address.
	PurposeEmailVerify Purpose = "email-verify"
)

// tokenVersion is the payload schema version, stamped so the format can evolve
// without silently accepting an incompatible older token.
const tokenVersion = 1

// Errors returned by Verify. Callers map every one of these to the same
// user-safe "invalid or expired link" response — never leak which check failed.
var (
	// ErrMalformed is returned for a token that is not the expected
	// "<payload>.<sig>" shape or whose segments do not base64url-decode.
	ErrMalformed = errors.New("notifytoken: malformed token")
	// ErrBadSignature is returned when the MAC does not match (wrong secret or
	// tampered payload).
	ErrBadSignature = errors.New("notifytoken: signature mismatch")
	// ErrExpired is returned for a well-signed token past its expiry.
	ErrExpired = errors.New("notifytoken: token expired")
	// ErrPurpose is returned when a token's purpose does not match the one the
	// caller required.
	ErrPurpose = errors.New("notifytoken: unexpected purpose")
	// ErrNoSecret is returned by NewSigner for an empty secret.
	ErrNoSecret = errors.New("notifytoken: empty signing secret")
)

// payload is the compact JSON body a token carries. Field names are terse to
// keep the encoded URL short.
type payload struct {
	V   int     `json:"v"`
	P   Purpose `json:"p"`
	U   string  `json:"u"`
	C   string  `json:"c,omitempty"`
	Em  string  `json:"em,omitempty"`
	Exp int64   `json:"e"`
}

// Claims is the verified content of a token.
type Claims struct {
	Purpose  Purpose
	UserID   string
	Category string
	// Email is the address a PurposeEmailVerify token proves control of.
	// Empty for every other purpose.
	Email string
}

// Signer mints and verifies tokens with a fixed HMAC-SHA256 secret. A zero
// Signer is invalid; build one with NewSigner. It is safe for concurrent use.
type Signer struct {
	secret []byte
	now    func() time.Time // injectable clock for tests; nil means time.Now
}

// NewSigner returns a Signer keyed by secret. An empty secret is rejected so a
// misconfigured deployment fails loudly rather than minting forgeable tokens.
func NewSigner(secret string) (*Signer, error) {
	if secret == "" {
		return nil, ErrNoSecret
	}
	return &Signer{secret: []byte(secret)}, nil
}

func (s *Signer) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// mac computes the base64url-encoded HMAC-SHA256 of the base64url payload
// segment.
func (s *Signer) mac(segment string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(segment))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// MintUnsubscribe mints a one-click unsubscribe token for (userID, category)
// valid for ttl. category is the lowercase notifpolicy category string
// (e.g. "workflow", "informational").
func (s *Signer) MintUnsubscribe(userID, category string, ttl time.Duration) (string, error) {
	return s.mint(payload{V: tokenVersion, P: PurposeUnsubscribe, U: userID, C: category, Exp: s.clock().Add(ttl).Unix()})
}

// MintPreferences mints a preferences deep-link token for userID valid for ttl.
func (s *Signer) MintPreferences(userID string, ttl time.Duration) (string, error) {
	return s.mint(payload{V: tokenVersion, P: PurposePreferences, U: userID, Exp: s.clock().Add(ttl).Unix()})
}

// MintEmailVerify mints a "verify your email" token for (userID, email) valid
// for ttl. email is the exact address the link proves control of; verifying
// re-checks it against the account's current address so a token minted for a
// since-changed address is never honored (see grpcsvc/consumer callers).
func (s *Signer) MintEmailVerify(userID, email string, ttl time.Duration) (string, error) {
	return s.mint(payload{V: tokenVersion, P: PurposeEmailVerify, U: userID, Em: email, Exp: s.clock().Add(ttl).Unix()})
}

func (s *Signer) mint(p payload) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	segment := base64.RawURLEncoding.EncodeToString(body)
	return segment + "." + s.mac(segment), nil
}

// Verify checks a token's signature, expiry, and purpose, returning its claims.
// want is the purpose the caller requires; a token minted for a different
// purpose is rejected with ErrPurpose.
func (s *Signer) Verify(token string, want Purpose) (Claims, error) {
	segment, sig, ok := strings.Cut(token, ".")
	if !ok || segment == "" || sig == "" {
		return Claims{}, ErrMalformed
	}
	// Constant-time MAC comparison before any decode/parse work.
	if !hmac.Equal([]byte(sig), []byte(s.mac(segment))) {
		return Claims{}, ErrBadSignature
	}
	body, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return Claims{}, ErrMalformed
	}
	if p.V != tokenVersion || p.U == "" {
		return Claims{}, ErrMalformed
	}
	if p.P != want {
		return Claims{}, ErrPurpose
	}
	if p.P == PurposeEmailVerify && p.Em == "" {
		return Claims{}, ErrMalformed
	}
	if s.clock().After(time.Unix(p.Exp, 0)) {
		return Claims{}, ErrExpired
	}
	return Claims{Purpose: p.P, UserID: p.U, Category: p.C, Email: p.Em}, nil
}
