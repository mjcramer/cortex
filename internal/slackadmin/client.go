package slackadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to Slack's app-configuration endpoints (tooling.tokens.* and
// apps.manifest.*). These require app-config tokens, not the bot token.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

func NewClient(baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://slack.com/api"
	}
	return &Client{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

type rotateResponse struct {
	OK           bool   `json:"ok"`
	Error        string `json:"error"`
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
	Exp          int64  `json:"exp"` // unix seconds
}

// Rotate exchanges a refresh token for a fresh access+refresh pair. It uses no
// bearer auth — the refresh token is the credential, sent in the form body.
func (c *Client) Rotate(ctx context.Context, refreshToken string) (token, newRefresh string, expiresAt time.Time, err error) {
	form := url.Values{"refresh_token": {refreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/tooling.tokens.rotate", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("tooling.tokens.rotate request: %w", err)
	}
	defer resp.Body.Close()

	var r rotateResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", "", time.Time{}, fmt.Errorf("decode rotate response: %w", err)
	}
	if !r.OK {
		return "", "", time.Time{}, fmt.Errorf("tooling.tokens.rotate: %s", r.Error)
	}
	return r.Token, r.RefreshToken, time.Unix(r.Exp, 0), nil
}

type updateResponse struct {
	OK                 bool   `json:"ok"`
	Error              string `json:"error"`
	AppID              string `json:"app_id"`
	PermissionsUpdated bool   `json:"permissions_updated"`
	// Errors carries the per-field detail Slack returns for invalid_manifest,
	// e.g. {pointer: "/oauth_config/scopes/bot", message: "..."}. Without this
	// the top-level "invalid_manifest" code alone is not actionable.
	Errors []struct {
		Message string `json:"message"`
		Pointer string `json:"pointer"`
	} `json:"errors"`
}

func (r updateResponse) errorString() string {
	if len(r.Errors) == 0 {
		return r.Error
	}
	parts := make([]string, 0, len(r.Errors))
	for _, e := range r.Errors {
		if e.Pointer != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", e.Message, e.Pointer))
		} else {
			parts = append(parts, e.Message)
		}
	}
	return fmt.Sprintf("%s: %s", r.Error, strings.Join(parts, "; "))
}

// UpdateManifest applies a manifest to the app, authenticating with the bearer
// access token. It returns whether scopes changed — when true, Slack requires a
// reinstall before the new permissions take effect.
func (c *Client) UpdateManifest(ctx context.Context, accessToken, appID string, manifest map[string]any) (permissionsUpdated bool, err error) {
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return false, fmt.Errorf("encode manifest: %w", err)
	}
	payload, err := json.Marshal(map[string]string{
		"app_id":   appID,
		"manifest": string(manifestJSON),
	})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/apps.manifest.update", bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("apps.manifest.update request: %w", err)
	}
	defer resp.Body.Close()

	var r updateResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return false, fmt.Errorf("decode update response: %w", err)
	}
	if !r.OK {
		return false, fmt.Errorf("apps.manifest.update: %s", r.errorString())
	}
	return r.PermissionsUpdated, nil
}
