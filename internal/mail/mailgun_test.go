// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	email "github.com/Bugs5382/go-email"
	"github.com/Steward-GRC/steward-obligations/internal/mail"
	"github.com/stretchr/testify/require"
)

// newStub stands up an httptest server that records every request it receives
// (after parsing the multipart body) and replies with the given status/body,
// so tests can assert on the request shape and the transport's classification
// of the response.
func newStub(status int, body string) (*httptest.Server, *[]*http.Request) {
	var got []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		got = append(got, r)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	return srv, &got
}

// capRT is an http.RoundTripper that captures the outgoing request and returns
// a canned 2xx, letting a test assert the region→host URL the transport built
// without any network I/O.
type capRT struct{ req *http.Request }

func (c *capRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.req = r
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"id":"<x@mg>","message":"Queued"}`)),
		Header:     make(http.Header),
	}, nil
}

func testMsg() email.Message {
	return email.Message{
		From:    "no-reply@example.org",
		To:      []string{"user@example.com"},
		Subject: "Hi",
		HTML:    "<p>hi</p>",
		Text:    "hi",
	}
}

// isTransient reports whether err (or anything it wraps) is a go-email
// TransientError, the disposition go-email's Retry acts on.
func isTransient(err error) bool {
	var te email.TransientError
	return errors.As(err, &te)
}

func TestMailgunTransport_Send_Success(t *testing.T) {
	srv, got := newStub(http.StatusOK, `{"id":"<20260814.1@mg>","message":"Queued. Thank you."}`)
	defer srv.Close()

	tr := mail.NewMailgunTransport(
		mail.MailgunConfig{APIKey: "key-x", Domain: "mg.example.org", Region: "us", FromAddress: "no-reply@example.org"},
		mail.WithMailgunBaseURL(srv.URL),
	)

	require.NoError(t, tr.Send(context.Background(), testMsg()))
	require.Equal(t, "<20260814.1@mg>", tr.LastMessageID())
	require.True(t, tr.Healthy(), "a 2xx send must leave the transport healthy")
	require.Equal(t, http.StatusOK, tr.LastStatus())

	require.Len(t, *got, 1)
	r := (*got)[0]
	require.Equal(t, http.MethodPost, r.Method)
	require.Equal(t, "/v3/mg.example.org/messages", r.URL.Path)

	u, p, ok := r.BasicAuth()
	require.True(t, ok, "request must carry HTTP Basic auth")
	require.Equal(t, "api", u)
	require.Equal(t, "key-x", p)

	require.Equal(t, "user@example.com", r.PostFormValue("to"))
	require.Equal(t, "Hi", r.PostFormValue("subject"))
	require.Equal(t, "<p>hi</p>", r.PostFormValue("html"))
}

// TestMailgunTransport_MultipartBody proves the body is a multipart/form-data
// document carrying from/to/cc/bcc/subject/html/text/Reply-To and any
// attachments as file parts.
func TestMailgunTransport_MultipartBody(t *testing.T) {
	srv, got := newStub(http.StatusOK, `{"id":"<m@mg>"}`)
	defer srv.Close()

	tr := mail.NewMailgunTransport(
		mail.MailgunConfig{APIKey: "k", Domain: "d", Region: "us", FromAddress: "fallback@x"},
		mail.WithMailgunBaseURL(srv.URL),
	)

	msg := email.Message{
		From:    "from@x",
		To:      []string{"a@x", "b@x"},
		Cc:      []string{"c@x"},
		Bcc:     []string{"d@x"},
		Subject: "Subj",
		HTML:    "<p>body</p>",
		Text:    "body",
		ReplyTo: "reply@x",
		Attachments: []email.Attachment{
			{Filename: "report.pdf", ContentType: "application/pdf", Content: []byte("%PDF-1.4 fake")},
		},
	}
	require.NoError(t, tr.Send(context.Background(), msg))

	require.Len(t, *got, 1)
	r := (*got)[0]
	require.True(t, strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data"),
		"expected multipart/form-data, got %q", r.Header.Get("Content-Type"))

	require.Equal(t, "from@x", r.PostFormValue("from"))
	require.Equal(t, []string{"a@x", "b@x"}, r.PostForm["to"])
	require.Equal(t, "c@x", r.PostFormValue("cc"))
	require.Equal(t, "d@x", r.PostFormValue("bcc"))
	require.Equal(t, "Subj", r.PostFormValue("subject"))
	require.Equal(t, "<p>body</p>", r.PostFormValue("html"))
	require.Equal(t, "body", r.PostFormValue("text"))
	require.Equal(t, "reply@x", r.PostFormValue("h:Reply-To"))

	require.NotNil(t, r.MultipartForm)
	files := r.MultipartForm.File["attachment"]
	require.Len(t, files, 1)
	require.Equal(t, "report.pdf", files[0].Filename)
}

// TestMailgunTransport_ListUnsubscribeHeaders proves the RFC 8058 one-click
// List-Unsubscribe headers survive the Mailgun API leg as h:<Name> form fields
// . Without this the prod Mailgun path would drop
// them even though the SMTP path renders them natively.
func TestMailgunTransport_ListUnsubscribeHeaders(t *testing.T) {
	srv, got := newStub(http.StatusOK, `{"id":"<m@mg>"}`)
	defer srv.Close()

	tr := mail.NewMailgunTransport(
		mail.MailgunConfig{APIKey: "k", Domain: "d", Region: "us", FromAddress: "fallback@x"},
		mail.WithMailgunBaseURL(srv.URL),
	)

	msg := email.Message{
		From:                "from@x",
		To:                  []string{"a@x"},
		Subject:             "Subj",
		HTML:                "<p>body</p>",
		ListUnsubscribe:     "<https://policy.example.org/notify/unsubscribe?token=abc>",
		ListUnsubscribePost: "List-Unsubscribe=One-Click",
		Headers:             map[string]string{"X-Custom": "v"},
	}
	require.NoError(t, tr.Send(context.Background(), msg))

	require.Len(t, *got, 1)
	r := (*got)[0]
	require.Equal(t, "<https://policy.example.org/notify/unsubscribe?token=abc>", r.PostFormValue("h:List-Unsubscribe"))
	require.Equal(t, "List-Unsubscribe=One-Click", r.PostFormValue("h:List-Unsubscribe-Post"))
	require.Equal(t, "v", r.PostFormValue("h:X-Custom"))
}

// TestMailgunTransport_FromFallback proves an empty Message.From falls back to
// the configured FromAddress.
func TestMailgunTransport_FromFallback(t *testing.T) {
	srv, got := newStub(http.StatusOK, `{"id":"<m@mg>"}`)
	defer srv.Close()

	tr := mail.NewMailgunTransport(
		mail.MailgunConfig{APIKey: "k", Domain: "d", Region: "us", FromAddress: "default@example.org"},
		mail.WithMailgunBaseURL(srv.URL),
	)

	msg := testMsg()
	msg.From = ""
	require.NoError(t, tr.Send(context.Background(), msg))
	require.Equal(t, "default@example.org", (*got)[0].PostFormValue("from"))
}

func TestMailgunTransport_Classification(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		wantAuth  bool
		wantTrans bool
	}{
		{"auth401", http.StatusUnauthorized, true, false},
		{"forbidden403", http.StatusForbidden, true, false},
		{"ratelimit429", http.StatusTooManyRequests, false, true},
		{"server500", http.StatusInternalServerError, false, true},
		{"server503", http.StatusServiceUnavailable, false, true},
		{"badreq422", http.StatusUnprocessableEntity, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := newStub(c.status, `{"message":"nope"}`)
			defer srv.Close()

			tr := mail.NewMailgunTransport(
				mail.MailgunConfig{APIKey: "k", Domain: "d", Region: "us"},
				mail.WithMailgunBaseURL(srv.URL),
			)
			err := tr.Send(context.Background(), testMsg())
			require.Error(t, err)
			require.Equal(t, c.wantAuth, errors.Is(err, mail.ErrMailgunAuth), "auth classification")
			require.Equal(t, c.wantTrans, isTransient(err), "transient classification")
			require.Equal(t, c.status, tr.LastStatus())

			// Auth + transient failures mark the transport unhealthy for the
			// Slice-C breaker; a permanent per-message 4xx (bad recipient) must
			// NOT drag the whole transport down.
			if c.wantAuth || c.wantTrans {
				require.False(t, tr.Healthy(), "auth/transient failure should be unhealthy")
			} else {
				require.True(t, tr.Healthy(), "per-message 4xx must not mark transport unhealthy")
			}
		})
	}
}

// TestMailgunTransport_NetworkError proves a dial/transport error (no HTTP
// response at all) is classified transient.
func TestMailgunTransport_NetworkError(t *testing.T) {
	srv, _ := newStub(http.StatusOK, `{}`)
	url := srv.URL
	srv.Close() // closed server → connection refused

	tr := mail.NewMailgunTransport(
		mail.MailgunConfig{APIKey: "k", Domain: "d", Region: "us"},
		mail.WithMailgunBaseURL(url),
	)
	err := tr.Send(context.Background(), testMsg())
	require.Error(t, err)
	require.True(t, isTransient(err), "network failure must be transient")
	require.False(t, tr.Healthy())
}

func TestMailgunTransport_RegionBaseURL(t *testing.T) {
	cases := []struct {
		region string
		host   string
	}{
		{"us", "api.mailgun.net"},
		{"", "api.mailgun.net"},
		{"eu", "api.eu.mailgun.net"},
		{"EU", "api.eu.mailgun.net"},
	}
	for _, c := range cases {
		t.Run("region_"+c.region, func(t *testing.T) {
			rt := &capRT{}
			tr := mail.NewMailgunTransport(
				mail.MailgunConfig{APIKey: "k", Domain: "d", Region: c.region},
				mail.WithMailgunHTTPClient(&http.Client{Transport: rt}),
			)
			require.NoError(t, tr.Send(context.Background(), testMsg()))
			require.NotNil(t, rt.req)
			require.Equal(t, "https", rt.req.URL.Scheme)
			require.Equal(t, c.host, rt.req.URL.Host)
			require.Equal(t, "/v3/d/messages", rt.req.URL.Path)
		})
	}
}
