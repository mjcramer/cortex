package slackadmin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// RegisterConfig holds everything Register needs to push the manifest. It is
// built from the environment by internal/config when auto-registration is
// enabled, and is nil otherwise.
type RegisterConfig struct {
	AppID        string
	CallbackURL  string
	RefreshToken string // bootstrap seed; only used on first run
	TokensPath   string
	ManifestPath string
	APIBaseURL   string
}

// Register pushes the local manifest to Slack so the app's request URLs and
// event subscriptions point at this server. It manages the rotating app-config
// token via a persisted state file, bootstrapping from cfg.RefreshToken on the
// first run and rotating transparently when the access token is near expiry.
func Register(ctx context.Context, cfg RegisterConfig, logger *slog.Logger) error {
	logger.Info("registering slack manifest",
		"app_id", cfg.AppID,
		"callback_url", cfg.CallbackURL,
		"manifest", cfg.ManifestPath,
		"tokens_path", cfg.TokensPath,
	)
	client := NewClient(cfg.APIBaseURL)

	state, bootstrapped, err := loadOrBootstrap(cfg)
	if err != nil {
		return err
	}
	if bootstrapped {
		logger.Info("no token state on disk; bootstrapping from SLACK_CONFIG_REFRESH_TOKEN",
			"tokens_path", cfg.TokensPath)
	} else {
		logger.Debug("loaded slack config token state",
			"tokens_path", cfg.TokensPath, "expires_at", state.ExpiresAt.Format(time.RFC3339))
	}

	if state.NeedsRotation() {
		logger.Info("rotating slack config token (expired or near expiry)",
			"expires_at", state.ExpiresAt.Format(time.RFC3339))
		token, refresh, exp, rerr := client.Rotate(ctx, state.RefreshToken)
		if rerr != nil {
			return fmt.Errorf("rotate config token: %w", rerr)
		}
		state.AccessToken = token
		state.RefreshToken = refresh
		state.ExpiresAt = exp
		if serr := state.Save(cfg.TokensPath); serr != nil {
			return fmt.Errorf("persist rotated token: %w", serr)
		}
		logger.Info("rotated and persisted slack config token",
			"path", cfg.TokensPath, "expires_at", exp.Format(time.RFC3339))
	} else {
		logger.Debug("slack config token still valid; skipping rotation",
			"expires_at", state.ExpiresAt.Format(time.RFC3339))
	}

	logger.Debug("loading slack manifest", "path", cfg.ManifestPath, "callback_url", cfg.CallbackURL)
	manifest, err := LoadManifest(cfg.ManifestPath, cfg.CallbackURL)
	if err != nil {
		return err
	}

	logger.Debug("pushing manifest to slack apps.manifest.update", "app_id", cfg.AppID)
	permsUpdated, err := client.UpdateManifest(ctx, state.AccessToken, cfg.AppID, manifest)
	if err != nil {
		return err
	}
	logger.Info("slack manifest registered",
		"app_id", cfg.AppID,
		"callback_url", cfg.CallbackURL,
		"permissions_updated", permsUpdated,
	)
	if permsUpdated {
		logger.Warn("slack scopes changed — reinstall the app to apply them", "app_id", cfg.AppID)
	}
	return nil
}

// loadOrBootstrap reads persisted token state, or seeds a fresh state from the
// bootstrap refresh token when no state file exists yet. The seeded state has a
// zero ExpiresAt so the caller rotates it immediately on first use.
func loadOrBootstrap(cfg RegisterConfig) (state *TokenState, bootstrapped bool, err error) {
	state, err = LoadTokenState(cfg.TokensPath)
	if err == nil {
		return state, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("load token state: %w", err)
	}
	if cfg.RefreshToken == "" {
		return nil, false, fmt.Errorf("no token state at %s and SLACK_CONFIG_REFRESH_TOKEN is unset (seed it once from api.slack.com)", cfg.TokensPath)
	}
	return &TokenState{AppID: cfg.AppID, RefreshToken: cfg.RefreshToken}, true, nil
}
