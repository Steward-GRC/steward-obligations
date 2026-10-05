// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package emailservice_test

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	corev1 "github.com/Steward-GRC/steward-obligations/gen/go/thirdparty/core/v1"
	"github.com/Steward-GRC/steward-obligations/internal/emailservice"
)

// fakeSecretClient stands in for corev1.EmailServiceSecretServiceClient. It counts
// calls (so tests can assert cache hits vs. gRPC round-trips) and returns a
// canned response or error.
type fakeSecretClient struct {
	mu    sync.Mutex
	calls int
	resp  *corev1.GetEmailServiceSecretResponse
	err   error
}

func (f *fakeSecretClient) GetEmailServiceSecret(_ context.Context, _ *corev1.GetEmailServiceSecretRequest, _ ...grpc.CallOption) (*corev1.GetEmailServiceSecretResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func (f *fakeSecretClient) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// memKV is an in-memory cache.KV that records the TTL of the last SetBytes so
// tests can assert the 60s TTL is applied. It mirrors the fake used by the
// obligation cache tests but adds TTL capture.
type memKV struct {
	mu      sync.Mutex
	m       map[string][]byte
	lastTTL time.Duration
	setN    int
	delN    int
}

func newMemKV() *memKV { return &memKV{m: map[string][]byte{}} }

func (k *memKV) GetBytes(_ context.Context, key string) ([]byte, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	b, ok := k.m[key]
	return b, ok, nil
}

func (k *memKV) SetBytes(_ context.Context, key string, val []byte, ttl time.Duration) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = val
	k.lastTTL = ttl
	k.setN++
	return nil
}

func (k *memKV) Del(_ context.Context, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, key)
	k.delN++
	return nil
}

func enabledResp(key string) *corev1.GetEmailServiceSecretResponse {
	return &corev1.GetEmailServiceSecretResponse{Config: &corev1.EmailServiceConfig{
		ApiKey:      key,
		Domain:      "mg.example.org",
		Region:      "us",
		FromAddress: "no-reply@example.org",
		Enabled:     true,
	}}
}

func TestGet_MissThenHit_CachesAndSkipsGRPC(t *testing.T) {
	client := &fakeSecretClient{resp: enabledResp("key-abc")}
	kv := newMemKV()
	p := emailservice.NewProvider(client, kv, 60*time.Second)
	ctx := context.Background()

	got, ok, err := p.Get(ctx)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if !ok {
		t.Fatalf("first Get ok=false, want true")
	}
	if got.APIKey != "key-abc" || got.Domain != "mg.example.org" || got.Region != "us" || got.FromAddress != "no-reply@example.org" {
		t.Fatalf("first Get config mismatch: %+v", got)
	}
	if client.count() != 1 {
		t.Fatalf("after first Get gRPC calls = %d, want 1", client.count())
	}

	// Second Get must be served from the cache: no additional gRPC call.
	got2, ok2, err := p.Get(ctx)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if !ok2 || got2.APIKey != "key-abc" {
		t.Fatalf("second Get mismatch: ok=%v cfg=%+v", ok2, got2)
	}
	if client.count() != 1 {
		t.Fatalf("after second Get gRPC calls = %d, want 1 (cache hit)", client.count())
	}
}

func TestGet_AppliesConfiguredTTL(t *testing.T) {
	client := &fakeSecretClient{resp: enabledResp("key-abc")}
	kv := newMemKV()
	p := emailservice.NewProvider(client, kv, 60*time.Second)

	if _, _, err := p.Get(context.Background()); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if kv.lastTTL != 60*time.Second {
		t.Fatalf("cache TTL = %v, want 60s", kv.lastTTL)
	}
}

func TestNewProvider_DefaultsTTL(t *testing.T) {
	client := &fakeSecretClient{resp: enabledResp("key-abc")}
	kv := newMemKV()
	p := emailservice.NewProvider(client, kv, 0) // 0 -> DefaultTTL

	if _, _, err := p.Get(context.Background()); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if kv.lastTTL != emailservice.DefaultTTL {
		t.Fatalf("cache TTL = %v, want DefaultTTL %v", kv.lastTTL, emailservice.DefaultTTL)
	}
}

func TestGet_DisabledConfig_OkFalse(t *testing.T) {
	// Enabled=false with a populated key must still be ok=false.
	client := &fakeSecretClient{resp: &corev1.GetEmailServiceSecretResponse{Config: &corev1.EmailServiceConfig{
		ApiKey:  "key-abc",
		Domain:  "mg.example.org",
		Enabled: false,
	}}}
	p := emailservice.NewProvider(client, newMemKV(), 60*time.Second)

	_, ok, err := p.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatalf("disabled config ok=true, want false")
	}
}

func TestGet_UnsetConfig_OkFalse(t *testing.T) {
	// The unset state core returns: enabled=false, empty key/domain.
	client := &fakeSecretClient{resp: &corev1.GetEmailServiceSecretResponse{Config: &corev1.EmailServiceConfig{
		Region: "us",
	}}}
	p := emailservice.NewProvider(client, newMemKV(), 60*time.Second)

	_, ok, err := p.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatalf("unset config ok=true, want false")
	}
}

func TestGet_EnabledButMissingFields_OkFalse(t *testing.T) {
	// Enabled but no api key -> cannot send -> ok=false.
	client := &fakeSecretClient{resp: &corev1.GetEmailServiceSecretResponse{Config: &corev1.EmailServiceConfig{
		Domain:  "mg.example.org",
		Enabled: true,
	}}}
	p := emailservice.NewProvider(client, newMemKV(), 60*time.Second)

	_, ok, err := p.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatalf("enabled-but-keyless config ok=true, want false")
	}
}

func TestGet_GRPCError_Propagates(t *testing.T) {
	wantErr := errors.New("core unavailable")
	client := &fakeSecretClient{err: wantErr}
	p := emailservice.NewProvider(client, newMemKV(), 60*time.Second)

	_, ok, err := p.Get(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Get err = %v, want %v", err, wantErr)
	}
	if ok {
		t.Fatalf("on error ok=true, want false")
	}
}

func TestGet_NilKV_AlwaysCallsGRPC(t *testing.T) {
	client := &fakeSecretClient{resp: enabledResp("key-abc")}
	p := emailservice.NewProvider(client, nil, 60*time.Second)
	ctx := context.Background()

	if _, _, err := p.Get(ctx); err != nil {
		t.Fatalf("Get 1: %v", err)
	}
	if _, _, err := p.Get(ctx); err != nil {
		t.Fatalf("Get 2: %v", err)
	}
	if client.count() != 2 {
		t.Fatalf("nil-KV gRPC calls = %d, want 2 (no cache)", client.count())
	}
}

func TestInvalidate_BustsCache(t *testing.T) {
	client := &fakeSecretClient{resp: enabledResp("key-abc")}
	kv := newMemKV()
	p := emailservice.NewProvider(client, kv, 60*time.Second)
	ctx := context.Background()

	if _, _, err := p.Get(ctx); err != nil {
		t.Fatalf("Get 1: %v", err)
	}
	if client.count() != 1 {
		t.Fatalf("calls after Get 1 = %d, want 1", client.count())
	}

	if err := p.Invalidate(ctx); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if kv.delN != 1 {
		t.Fatalf("Del calls = %d, want 1", kv.delN)
	}

	// After a bust the next Get must re-fetch from core.
	if _, _, err := p.Get(ctx); err != nil {
		t.Fatalf("Get 2: %v", err)
	}
	if client.count() != 2 {
		t.Fatalf("calls after bust+Get = %d, want 2", client.count())
	}
}

func TestInvalidate_NilKV_NoError(t *testing.T) {
	p := emailservice.NewProvider(&fakeSecretClient{resp: enabledResp("k")}, nil, 60*time.Second)
	if err := p.Invalidate(context.Background()); err != nil {
		t.Fatalf("Invalidate nil KV: %v", err)
	}
}

// TestGet_APIKeyNeverLogged captures stdout (go-log writes there) at debug level
// across the full miss->grpc->cache->hit->error cycle and asserts the api key
// never appears in any log line.
func TestGet_APIKeyNeverLogged(t *testing.T) {
	const sentinel = "SUPER-SECRET-MAILGUN-KEY-DO-NOT-LOG"
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FORMAT", "json")

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	// Success path (miss -> grpc -> cache), then a cache hit, then an error path.
	kv := newMemKV()
	okClient := &fakeSecretClient{resp: enabledResp(sentinel)}
	p := emailservice.NewProvider(okClient, kv, 60*time.Second)
	_, _, _ = p.Get(context.Background())
	_, _, _ = p.Get(context.Background())

	errClient := &fakeSecretClient{err: errors.New("boom")}
	pErr := emailservice.NewProvider(errClient, newMemKV(), 60*time.Second)
	_, _, _ = pErr.Get(context.Background())

	_ = w.Close()
	os.Stdout = orig
	logged := <-done

	if strings.Contains(logged, sentinel) {
		t.Fatalf("api key leaked to logs:\n%s", logged)
	}
}
