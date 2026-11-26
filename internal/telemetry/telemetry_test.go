package telemetry_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/telemetry"
)

func TestTokenAndEnabled(t *testing.T) {
	for _, tc := range []struct {
		token, send string
		want        bool
	}{
		{"", "", false},
		{"  \t ", "", false},
		{"pylf_v1_us_abc", "no", false},
		{"pylf_v1_us_abc", "FALSE", false},
		{"pylf_v1_us_abc", "", true},
	} {
		t.Setenv("LOGFIRE_TOKEN", tc.token)
		t.Setenv("LOGFIRE_SEND_TO_LOGFIRE", tc.send)
		if got := telemetry.Enabled(); got != tc.want {
			t.Errorf("token=%q send=%q: got %v, want %v", tc.token, tc.send, got, tc.want)
		}
	}
}

func TestDisabledSessionIsInert(t *testing.T) {
	t.Setenv("LOGFIRE_TOKEN", "")
	s := telemetry.Init(context.Background())
	if s.Active() {
		t.Fatal("expected inactive session when LOGFIRE_TOKEN is empty")
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

	t.Setenv("LOGFIRE_TOKEN", "pylf_v1_us_testtoken")
	t.Setenv("LOGFIRE_SEND_TO_LOGFIRE", "")
	t.Setenv("LOGFIRE_BASE_URL", collector.URL)
	t.Setenv("LOGFIRE_ENVIRONMENT", "test")

	s := telemetry.Init(context.Background())
	if !s.Active() {
		t.Fatal("expected active telemetry session")
	}

	const (
		secretHandle   = "alice.secret.example"
		secretDID      = "did:plc:secret12345"
		secretPassword = "super-secret-app-password"
		secretJWT      = "super-secret-jwt-token"
		secretErrMsg   = "bad handle alice.secret.example for did:plc:secret12345"
	)

	pds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/xrpc/com.atproto.server.createSession":
			_, _ = io.WriteString(w, `{"accessJwt":"`+secretJWT+`","did":"`+secretDID+`","handle":"`+secretHandle+`"}`)
		case "/xrpc/app.bsky.actor.getProfile":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"InvalidRequest","message":"`+secretErrMsg+`"}`)
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
		"InvalidRequest",
		"xrpc.outcome",
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
		secretErrMsg,
		pds.URL,
	} {
		if bytes.Contains(payload, []byte(secret)) {
			t.Errorf("exported payload leaked sensitive string %q", secret)
		}
	}
}
