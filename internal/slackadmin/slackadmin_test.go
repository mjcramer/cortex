package slackadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadManifestSubstitutesCallbackURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	const body = `display_information:
  name: Cortex
settings:
  event_subscriptions:
    request_url: ${SLACK_CALLBACK_URL}/slack/events
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// A trailing slash on the base must not produce a double slash.
	m, err := LoadManifest(path, "https://example.ngrok.dev/")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}

	settings, _ := m["settings"].(map[string]any)
	subs, _ := settings["event_subscriptions"].(map[string]any)
	got, _ := subs["request_url"].(string)
	if want := "https://example.ngrok.dev/slack/events"; got != want {
		t.Fatalf("request_url = %q, want %q", got, want)
	}
}

func TestLoadManifestRejectsEmptyCallbackURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte("url: ${SLACK_CALLBACK_URL}/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path, "/"); err == nil {
		t.Fatal("expected error for empty callback base, got nil")
	}
}

func TestTokenStateSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "slack-tokens.json")
	want := &TokenState{
		AppID:        "A123",
		AccessToken:  "xoxe-access",
		RefreshToken: "xoxe-refresh",
		ExpiresAt:    time.Now().Add(12 * time.Hour).Truncate(time.Second),
	}
	if err := want.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token file perms = %o, want 600", perm)
	}

	got, err := LoadTokenState(path)
	if err != nil {
		t.Fatalf("LoadTokenState: %v", err)
	}
	if got.AppID != want.AppID || got.AccessToken != want.AccessToken ||
		got.RefreshToken != want.RefreshToken || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestNeedsRotation(t *testing.T) {
	cases := []struct {
		name string
		s    TokenState
		want bool
	}{
		{"empty access token", TokenState{ExpiresAt: time.Now().Add(time.Hour)}, true},
		{"near expiry", TokenState{AccessToken: "x", ExpiresAt: time.Now().Add(30 * time.Second)}, true},
		{"fresh", TokenState{AccessToken: "x", ExpiresAt: time.Now().Add(time.Hour)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.NeedsRotation(); got != tc.want {
				t.Fatalf("NeedsRotation() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUpdateManifestSendsManifestAsJSONString(t *testing.T) {
	var got struct {
		AppID    string `json:"app_id"`
		Manifest any    `json:"manifest"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apps.manifest.update" {
			t.Fatalf("path = %q, want /apps.manifest.update", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer xoxe-access" {
			t.Fatalf("Authorization = %q", auth)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"permissions_updated":false}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	_, err := client.UpdateManifest(context.Background(), "xoxe-access", "A123", map[string]any{
		"display_information": map[string]any{"name": "Cortex"},
	})
	if err != nil {
		t.Fatalf("UpdateManifest: %v", err)
	}
	if got.AppID != "A123" {
		t.Fatalf("app_id = %q, want A123", got.AppID)
	}
	manifestString, ok := got.Manifest.(string)
	if !ok {
		t.Fatalf("manifest type = %T, want string", got.Manifest)
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(manifestString), &manifest); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
}
