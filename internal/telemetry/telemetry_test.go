package telemetry_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/telemetry"
)

func TestEnabled(t *testing.T) {
	for _, tc := range []struct {
		endpoint, headers string
		want              bool
	}{
		{"", "", false},
		{"https://logfire-us.pydantic.dev", "", false},
		{"", "Authorization=tok", false},
		{"  \t ", "Authorization=tok", false},
		{"https://logfire-us.pydantic.dev", "Authorization=tok", true},
	} {
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", tc.endpoint)
		t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", tc.headers)
		if got := telemetry.Enabled(); got != tc.want {
			t.Errorf("endpoint=%q headers=%q: got %v, want %v", tc.endpoint, tc.headers, got, tc.want)
		}
	}
}

func TestClassifyOutcome(t *testing.T) {
	if got := telemetry.ClassifyOutcome(nil); got != "ok" {
		t.Errorf("nil = %q", got)
	}
	if got := telemetry.ClassifyOutcome(context.DeadlineExceeded); got != "timeout" {
		t.Errorf("deadline = %q", got)
	}
	if got := telemetry.ClassifyOutcome(context.Canceled); got != "canceled" {
		t.Errorf("canceled = %q", got)
	}
	if got := telemetry.ClassifyOutcome(errors.New("boom")); got != "operation_failed" {
		t.Errorf("other = %q", got)
	}
}

func TestDisabledSessionIsInert(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "")
	s, err := telemetry.Init(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Active() {
		t.Fatal("expected inactive session when OTLP env vars are unset")
	}
	s.Shutdown()
}

func TestExportedSpansScrubSensitiveData(t *testing.T) {
	var (
		mu       sync.Mutex
		exported []byte
	)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		exported = append(exported, b...)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=pylf_v1_us_testtoken")
	t.Setenv("OTEL_SERVICE_NAME", "bskyban-test")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment.name=test")

	s, err := telemetry.Init(context.Background())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !s.Active() {
		t.Fatal("expected active telemetry session")
	}

	const (
		secretHandle   = "alice.secret.example"
		secretDID      = "did:plc:secret12345"
		secretPassword = "super-secret-app-password"
		secretJWT      = "super-secret-jwt-token"
		secretErrCode  = "SecretServerErrorCode"
		secretErrMsg   = "bad handle alice.secret.example for did:plc:secret12345"
	)

	pds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/xrpc/com.atproto.server.createSession":
			_, _ = io.WriteString(w, `{"accessJwt":"`+secretJWT+`","did":"`+secretDID+`","handle":"`+secretHandle+`"}`)
		case "/xrpc/app.bsky.actor.getProfile":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"`+secretErrCode+`","message":"`+secretErrMsg+`"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(pds.Close)

	c := bsky.NewClient(pds.URL)
	if err := c.Login(context.Background(), secretHandle, secretPassword); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := c.GetProfile(context.Background(), secretDID); err == nil {
		t.Fatal("expected GetProfile error")
	}

	s.Shutdown()

	mu.Lock()
	payload := exported
	mu.Unlock()
	if len(payload) == 0 {
		t.Fatal("expected exported OTLP payload bytes")
	}

	for _, want := range []string{
		"com.atproto.server.createSession",
		"app.bsky.actor.getProfile",
		"operation_failed",
		"xrpc.outcome",
		"bskyban-test",
	} {
		if !bytes.Contains(payload, []byte(want)) {
			t.Errorf("exported payload missing expected %q", want)
		}
	}
	for _, secret := range []string{
		secretHandle,
		secretDID,
		secretPassword,
		secretJWT,
		secretErrCode,
		secretErrMsg,
		pds.URL,
	} {
		if bytes.Contains(payload, []byte(secret)) {
			t.Errorf("exported payload leaked sensitive string %q", secret)
		}
	}
}
