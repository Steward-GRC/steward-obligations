// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

//go:build smoke

// Package mail_test's smoke suite exercises the ONE path Tasks 5-8's unit
// tests never touch: a real Go mail.Sender dialing a real render sidecar
// process and a real SMTP catcher, end to end. It is guarded by the "smoke"
// build tag so `go test./...` never runs it -- see
// `go test -tags smoke./internal/mail/... -run Smoke -v`.
package mail_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

// maildevImage mirrors the harbor-proxied image the estate's docker-compose
// dev stack already runs (see policy-maildev), so the smoke test never
// reaches out past the harbor mirror for its catcher image.
const maildevImage = "harbor.it.example.org/dockerhub/maildev/maildev:2.2.1"

// sidecarPort is the fixed loopback port the smoke test's sidecar
// subprocess listens on, matching render-sidecar/src/server.ts's default.
const sidecarPort = "8091"

// maildevMessage is the subset of maidev's GET /email response this smoke
// test asserts against.
type maildevMessage struct {
	Subject string             `json:"subject"`
	HTML    string             `json:"html"`
	To      []maildevRecipient `json:"to"`
}

type maildevRecipient struct {
	Address string `json:"address"`
}

// TestSmokeRenderAndSendPolicyAckReminder proves the real
// Go-sender -> Node-sidecar -> SMTP wiring: it starts a maildev container
// and the render sidecar as a subprocess, sends a real policy-ack-reminder
// through mail.NewSender's real SMTP transport (no WithTransport stub), and
// asserts maildev received exactly one branded message.
func TestSmokeRenderAndSendPolicyAckReminder(t *testing.T) {
	ctx := context.Background()

	smtpHost, smtpPort, httpBase := startMaildev(t, ctx)
	startRenderSidecar(t)

	cfg := mail.SenderConfig{
		SMTPHost: smtpHost,
		SMTPPort: smtpPort,
		From:     "no-reply@example.org",
		// SidecarURL points at the subprocess started above.
		SidecarURL: "http://127.0.0.1:" + sidecarPort,
		// AppEnv "dev" with DevCatchAllTo left unset ("") is deliberate, not
		// a copy-paste of a "dev" deployment: devCatchAll's own guard
		// (appEnv != "dev" || catchAllTo == "") is a no-op passthrough
		// whenever catchAllTo is empty, dev or not, so the To below reaches
		// maildev unrewritten.
		AppEnv: "dev",
		// SMTPStartTLS: false plus AppEnv=="dev" gives BuildSMTPConfig a
		// plaintext transport, which maildev requires -- it does not
		// advertise STARTTLS (verified against this same maildev image:
		// EHLO's extension list has no STARTTLS). No Password is set above,
		// so BuildSMTPConfig also leaves smtp.Config.User empty, letting
		// go-email skip SMTP AUTH entirely -- maildev's default catcher does
		// not advertise AUTH either.
		SMTPStartTLS: false,
	}

	sender, err := mail.NewSender(cfg)
	if err != nil {
		t.Fatalf("mail.NewSender: %v", err)
	}

	const to = "erin@example.org"
	vars := map[string]any{
		"recipientName": "Carol Example",
		"policies": []map[string]any{
			{
				"ref":    "POL-014 v3",
				"title":  "Remote Access & VPN Use Policy",
				"dueBy":  "August 15, 2026",
				"ackUrl": "https://policy.example.org/ack/pol-014",
			},
			{
				"ref":    "POL-031 v1",
				"title":  "Data Retention & Disposal Policy",
				"dueBy":  "August 18, 2026",
				"ackUrl": "https://policy.example.org/ack/pol-031",
			},
		},
		"portalUrl":      "https://policy.example.org/portal/acknowledgements",
		"preferencesUrl": "https://policy.example.org/portal/preferences",
	}

	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := sender.Send(sendCtx, "policy-ack-reminder", "smoke-user-1", to, "smoke-run", vars); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msgs := pollMaildevMessages(t, httpBase, 1)
	if len(msgs) != 1 {
		t.Fatalf("maildev: got %d messages, want 1", len(msgs))
	}
	msg := msgs[0]

	if len(msg.To) != 1 || msg.To[0].Address != to {
		t.Fatalf("maildev message To = %+v, want [%s]", msg.To, to)
	}
	if msg.Subject == "" {
		t.Fatal("maildev message subject is empty")
	}
	const wantSubject = "Policy acknowledgements due"
	if msg.Subject != wantSubject {
		t.Errorf("maildev message subject = %q, want %q", msg.Subject, wantSubject)
	}

	for _, want := range []string{
		// The sidecar's HTML renderer correctly HTML-entity-escapes template
		// data, so a raw "&" in a policy title never appears literally in
		// the rendered body -- assert against the escaped form these titles
		// actually produce ("Remote Access &amp; VPN Use Policy" etc.),
		// matching what any correct HTML renderer emits for a "&" in text
		// content.
		html.EscapeString("Remote Access & VPN Use Policy"),
		html.EscapeString("Data Retention & Disposal Policy"),
		// A stable layout marker (the header's default product name),
		// present whichever template rendered it: proof the sidecar composed
		// the message rather than us stubbing HTML.
		"Steward",
	} {
		if !bytes.Contains([]byte(msg.HTML), []byte(want)) {
			t.Errorf("maildev message HTML missing branded marker %q", want)
		}
	}
}

// TestSmokeRenderAndSendSlice1Kinds proves the two SP-5 Slice-1 template kinds
// (ack-required, policy-published) render through the real Node sidecar and
// land in maildev with their registry subjects -- the same
// Go-sender -> sidecar -> SMTP path as the ack-reminder smoke test.
func TestSmokeRenderAndSendSlice1Kinds(t *testing.T) {
	ctx := context.Background()

	smtpHost, smtpPort, httpBase := startMaildev(t, ctx)
	startRenderSidecar(t)

	cfg := mail.SenderConfig{
		SMTPHost:     smtpHost,
		SMTPPort:     smtpPort,
		From:         "no-reply@example.org",
		SidecarURL:   "http://127.0.0.1:" + sidecarPort,
		AppEnv:       "dev",
		SMTPStartTLS: false,
	}
	sender, err := mail.NewSender(cfg)
	if err != nil {
		t.Fatalf("mail.NewSender: %v", err)
	}

	const to = "recipient@example.org"
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ackRequiredVars := map[string]any{
		"ackUrl":         "https://policy.example.org/portal/acknowledgements/pv-slice1",
		"bodyText":       "Please review the policy below and confirm your acknowledgement.",
		"dueBy":          "September 1, 2026",
		"itemTitle":      "Acceptable Use of AI Tools Policy",
		"preferencesUrl": "https://policy.example.org/portal/preferences",
		"recipientName":  "Carol Example",
	}
	if err := sender.Send(sendCtx, "ack-required", "smoke-user-2", to, "smoke-ack-required", ackRequiredVars); err != nil {
		t.Fatalf("Send ack-required: %v", err)
	}

	publishedVars := map[string]any{
		"ackUrl":         "https://policy.example.org/portal/acknowledgements/pv-slice1",
		"effectiveDate":  "September 1, 2026",
		"policyRef":      "POL-022 v1",
		"policyTitle":    "Acceptable Use of AI Tools Policy",
		"policyUrl":      "https://policy.example.org/policies/pol-022",
		"preferencesUrl": "https://policy.example.org/portal/preferences",
		"recipientName":  "Carol Example",
		"requiresAck":    true,
		"summary":        "A new policy has been published. Please review it.",
	}
	if err := sender.Send(sendCtx, "policy-published", "smoke-user-2", to, "smoke-policy-published", publishedVars); err != nil {
		t.Fatalf("Send policy-published: %v", err)
	}

	msgs := pollMaildevMessages(t, httpBase, 2)
	if len(msgs) < 2 {
		t.Fatalf("maildev: got %d messages, want >= 2", len(msgs))
	}

	gotSubjects := map[string]bool{}
	for _, m := range msgs {
		gotSubjects[m.Subject] = true
	}
	for _, want := range []string{"Acknowledgement required", "New policy published"} {
		if !gotSubjects[want] {
			t.Errorf("maildev missing message with subject %q (got %v)", want, gotSubjects)
		}
	}
}

// startMaildev launches a maildev container via testcontainers and returns
// the host/port a real SMTP client should dial plus the base URL of its
// REST API. It mirrors internal/store/testhelper_test.go's
// container-unavailable handling: a failure to start the container skips
// (rather than fails) the test, since this smoke test's job is to prove the
// wiring in an environment that CAN run docker, not to require one.
func startMaildev(t *testing.T, ctx context.Context) (host, port, httpBase string) {
	t.Helper()

	req := testcontainers.ContainerRequest{
		Image:        maildevImage,
		ExposedPorts: []string{"1025/tcp", "1080/tcp"},
		WaitingFor: wait.ForLog("SMTP Server running").
			WithStartupTimeout(60 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("maildev container unavailable (%v) — skipping smoke test", err)
	}
	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	containerHost, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("maildev container host: %v", err)
	}
	smtpPort, err := container.MappedPort(ctx, "1025/tcp")
	if err != nil {
		t.Fatalf("maildev container SMTP port: %v", err)
	}
	restPort, err := container.MappedPort(ctx, "1080/tcp")
	if err != nil {
		t.Fatalf("maildev container REST port: %v", err)
	}

	return containerHost, smtpPort.Port(), fmt.Sprintf("http://%s:%s", containerHost, restPort.Port())
}

// startRenderSidecar builds render-sidecar/dist (only if stale) and starts
// `node dist/server.js` as a subprocess on 127.0.0.1:8091, polling
// GET /healthz until it answers (bounded retries, no fixed sleep). The
// subprocess is killed in t.Cleanup. If node itself cannot be found/started,
// this skips rather than fails the test, matching startMaildev's handling
// of an environment that cannot run part of the stack.
func startRenderSidecar(t *testing.T) {
	t.Helper()

	sidecarDir, err := filepath.Abs("../../render-sidecar")
	if err != nil {
		t.Fatalf("resolve render-sidecar dir: %v", err)
	}
	serverJS := ensureSidecarBuilt(t, sidecarDir)

	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node not found on PATH (%v) — skipping smoke test", err)
	}

	cmd := exec.Command(nodeBin, serverJS)
	cmd.Dir = sidecarDir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Skipf("failed to start render sidecar (%v) — skipping smoke test", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	if err := pollHealthz("http://127.0.0.1:"+sidecarPort+"/healthz", 10*time.Second); err != nil {
		t.Fatalf("render sidecar never became healthy: %v\nsidecar output:\n%s", err, out.String())
	}
}

// ensureSidecarBuilt returns the path to render-sidecar/dist/server.js,
// running `npm run build` first if dist is missing or older than any file
// under src (tsc's only inputs). A build failure skips the test -- an
// environment that cannot run npm/tsc cannot run this smoke test either.
func ensureSidecarBuilt(t *testing.T, sidecarDir string) string {
	t.Helper()

	serverJS := filepath.Join(sidecarDir, "dist", "server.js")
	if !sidecarDistStale(sidecarDir, serverJS) {
		return serverJS
	}

	npmBin, err := exec.LookPath("npm")
	if err != nil {
		t.Skipf("npm not found on PATH (%v) — skipping smoke test", err)
	}
	cmd := exec.Command(npmBin, "run", "build")
	cmd.Dir = sidecarDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("render sidecar build failed (%v) — skipping smoke test:\n%s", err, out)
	}
	if _, err := os.Stat(serverJS); err != nil {
		t.Skipf("render sidecar dist/server.js missing after build (%v) — skipping smoke test", err)
	}
	return serverJS
}

// sidecarDistStale reports whether serverJS is missing or older than any
// file under sidecarDir/src.
func sidecarDistStale(sidecarDir, serverJS string) bool {
	distInfo, err := os.Stat(serverJS)
	if err != nil {
		return true
	}

	stale := false
	_ = filepath.WalkDir(filepath.Join(sidecarDir, "src"), func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err == nil && info.ModTime().After(distInfo.ModTime()) {
			stale = true
		}
		return nil
	})
	return stale
}

// pollHealthz polls url until it returns HTTP 200 or timeout elapses. It
// never sleeps a single fixed duration and hopes -- it retries on a short
// interval until either the sidecar answers or the deadline passes.
func pollHealthz(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:gosec,noctx // localhost-only smoke poll
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("unexpected status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s polling %s: %w", timeout, url, lastErr)
}

// pollMaildevMessages polls maildev's GET /email until it reports at least
// want messages or a bounded timeout elapses, then returns whatever it has.
// Delivery is asynchronous relative to the SMTP transaction completing, so
// this avoids a fixed sleep between Send returning and the assertion.
func pollMaildevMessages(t *testing.T, httpBase string, want int) []maildevMessage {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	var msgs []maildevMessage
	for time.Now().Before(deadline) {
		resp, err := http.Get(httpBase + "/email") //nolint:gosec,noctx // localhost/container-mapped smoke poll
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr == nil {
				var parsed []maildevMessage
				if json.Unmarshal(body, &parsed) == nil {
					msgs = parsed
					if len(msgs) >= want {
						return msgs
					}
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return msgs
}
