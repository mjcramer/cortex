# Slack sync — automation

> **Status (2026-06):** the token-rotating registration described below is now
> implemented in `internal/slackadmin` and runs from the **server itself** on
> startup when `CORTEX_SLACK_AUTOREGISTER=true`. Bootstrap once with
> `SLACK_CONFIG_REFRESH_TOKEN`, then the server rotates and persists the
> app-config token to `$XDG_CONFIG_HOME/cortex/slack-tokens.json` automatically.
> See the env vars in `CLAUDE.md` and the setup guide in `slack.md`.
>
> There is **no** `make slack-sync` target — the earlier shallow
> `sed | yq | jq | curl` pipeline was removed once the server handled
> registration itself (it carried the same obfuscation we set out to avoid and a
> second code path that could drift). The `validate`/`diff`/`--dry-run` modes and
> the standalone `cmd/slack-sync` CLI sketched below remain future work.

This doc captures that remaining design: an out-of-band tool whose distinct value
is **CI / manifest-as-code** — pushing the manifest from a pipeline (without
booting the server) so the deployed app config cannot drift from git. The server
auto-registration covers the everyday workflow; this would cover enforcement.

## Goals

1. No 12-hour token babysitting — the tool transparently refreshes when the access token is near expiry.
2. Detect scope changes before pushing and warn the user that a reinstall is needed.
3. `--dry-run` against `apps.manifest.validate` so you can preview the diff before applying.
4. Optionally diff the live manifest (via `apps.manifest.export`) against the local YAML so you see *what* would change.
5. CI-friendly: same flow works from GitHub Actions with a stored refresh token in Secret Manager.

## Why we backed off the first time

When this came up we started building `cmd/slack-sync/` as a Go binary. That was the wrong shape for "shallow." Going to Go is correct *once* we need token persistence, scope diffing, and validate/diff/apply modes — those are awkward in a Makefile but clean in 300 lines of Go.

## Sketch of the Go version

```
cmd/slack-sync/main.go              entry point, flags, orchestration
internal/slackadmin/
  client.go                         HTTP client for apps.manifest.* + tooling.tokens.rotate
  tokens.go                         persistent TokenState (load/save/rotate-if-needed)
  manifest.go                       YAML load + env templating + JSON conversion + scope diff
```

### TokenState

```go
type TokenState struct {
    AppID        string    `json:"app_id"`
    AccessToken  string    `json:"access_token"`
    RefreshToken string    `json:"refresh_token"`   // single-use, must persist after each rotation
    ExpiresAt    time.Time `json:"expires_at"`
}
```

Stored at `$XDG_CONFIG_HOME/cortex/slack-tokens.json` with mode `0600`. Bootstrap on first run from `SLACK_CONFIG_REFRESH_TOKEN`; after that the file is canonical.

### Token rotation

If `time.Until(state.ExpiresAt) < 60s`, POST to `tooling.tokens.rotate` (`refresh_token` as form-encoded body, no bearer auth) → returns new `token`, `refresh_token`, `iat`, `exp` → write back atomically (`.tmp` + `rename`).

### Endpoints used

| Endpoint | Auth | Purpose |
|---|---|---|
| `tooling.tokens.rotate` | refresh token in body | exchange refresh for fresh access+refresh pair |
| `apps.manifest.export` | bearer access token | fetch current installed manifest for diffing |
| `apps.manifest.validate` | bearer access token | dry-run schema + URL check |
| `apps.manifest.update` | bearer access token | apply new manifest |

### Scope diff for "needs reinstall" warning

```go
type ScopeDiff struct{ Added, Removed []string }
func DiffBotScopes(old, new []string) ScopeDiff
```

`apps.manifest.update` itself returns `permissions_updated: true` when scopes change — we can use that as the post-apply trigger instead of computing the diff ourselves. The pre-apply diff is only useful for `--dry-run`.

### CLI surface

```
slack-sync [--manifest path] [--dry-run] [--tokens path]

Required env:
  SLACK_APP_ID            App to update
  SLACK_CALLBACK_URL     Base HTTPS URL (e.g. https://your.ngrok-free.dev)

First-run only:
  SLACK_CONFIG_REFRESH_TOKEN
                          Seed refresh token from api.slack.com (after first
                          successful run, state is read from $tokens file)
```

### Make target swap

```makefile
slack-sync: ## Push manifest via cmd/slack-sync
	$(GO) run ./cmd/slack-sync $(SLACK_SYNC_ARGS)
```

## When to actually build this

When at least one of:
- You're updating the manifest more than ~twice a week.
- You want CI to enforce that the deployed manifest matches what's in git.
- You're moving to multi-environment apps (dev/staging/prod) and the per-env URL templating gets out of hand for the shell pipeline.

Until then, the shallow Makefile target gets you 90% of the value at 5% of the code.
