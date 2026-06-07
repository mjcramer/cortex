# Slack setup

Cortex uses Slack as its first human-messaging adapter. Each agent gets its own channel (`#<prefix><agent>`, prefix defaults to `agent-`), the bot auto-creates that channel on first `SendEvent`, and humans reply in the message thread. Inbound replies route back to the waiting session by `(channel_id, thread_ts)`.

This is the single source of truth for everything Slack-specific: the runtime message flow, app creation, credentials, getting Slack to call your server (automatically or manually), the Slack-related environment variables, and troubleshooting.

## How it works (runtime flow)

1. An agent calls `SendEvent` with an `agent`, `session_id`, `repo`, and `message`.
2. Cortex finds or creates `#<SLACK_CHANNEL_PREFIX><agent>` and posts the request there, using the agent name as the bot display name.
3. A human replies in the Slack thread like they would to any other teammate.
4. Slack sends the message event payload to `POST /slack/events`.
5. Cortex verifies the Slack signature, matches `(channel_id, thread_ts)` back to the session, records the response, and unblocks `WaitForResponse`.

## Setup at a glance

Two parts, with different automation stories:

- **One-time bootstrap (manual, unavoidable):** create the app, install it into the workspace, and copy out the bot token + signing secret. Installation is an OAuth consent flow — a human in the workspace must approve it, so this cannot be automated.
- **Pointing Slack at your server (automatable):** the request URLs and event subscriptions. This is either pushed automatically by the server on startup (recommended — see [Option A](#option-a-automatic-registration-recommended)) or set by hand in the Slack UI ([Option B](#option-b-manual-registration)).

The order matters: Slack verifies an `event_subscriptions.request_url` the moment it is set, by POSTing a signed challenge to it. So your server must be running and publicly reachable (tunnel up) before that URL is registered — which is why registration happens after the listener is live.

## 1. Create the Slack app

The app manifest lives at [`manifest.yaml`](manifest.yaml). Its URLs are templated with `${SLACK_CALLBACK_URL}`, so render a concrete copy first (the Slack UI will not accept an unresolved placeholder):

```bash
SLACK_CALLBACK_URL=https://your-domain.ngrok-free.dev \
  envsubst < integrations/slack/manifest.yaml
# or, without envsubst:
sed 's|${SLACK_CALLBACK_URL}|https://your-domain.ngrok-free.dev|g' \
  integrations/slack/manifest.yaml
```

Then:

1. Go to <https://api.slack.com/apps> and click **Create New App** → **From an app manifest**.
2. Pick the workspace you want to test against.
3. Paste the rendered manifest and create the app. You can edit any field later under **App Manifest**.

> If your server + tunnel are not up yet, Slack will fail to verify the `event_subscriptions.request_url` at create time. Either start them first (see [step 3](#3-start-cortex-and-expose-it-over-https)), or create the app from a manifest with the `settings.event_subscriptions` block removed and let auto-registration add it later.

What the scopes are for:

| Scope                  | Why Cortex needs it                                                                 |
| ---------------------- | ----------------------------------------------------------------------------------- |
| `chat:write`           | Post the agent's messages into the channel.                                         |
| `chat:write.customize` | Override `username` per call so each agent shows its own display name.              |
| `channels:manage`      | Create channels via `conversations.create` and archive them via `conversations.archive` on `/cortex agent destroy`. |
| `channels:read`        | Call `conversations.list` to find a pre-existing channel when `name_taken` fires.   |
| `channels:join`        | Call `conversations.join` so the bot can post into channels it didn't create.       |
| `commands`             | Receive `/cortex` slash command invocations.                                        |

## 2. Install the app into the workspace

1. Open **Settings → Install App** and click **Install to Workspace**.
2. Approve the scopes.
3. Copy the **Bot User OAuth Token** (`xoxb-...`) — this is `SLACK_BOT_TOKEN`.
4. Open **Settings → Basic Information** and copy the **Signing Secret** — this is `SLACK_SIGNING_SECRET`.

## 3. Start Cortex and expose it over HTTPS

Slack needs to reach your `/slack/events` endpoint over HTTPS, and it signs every request — including the URL verification challenge — so the server has to be running with the real signing secret before Slack will accept the URL.

In one shell:

```bash
export SLACK_BOT_TOKEN=xoxb-...
export SLACK_SIGNING_SECRET=...                    # from Basic Information
export ANTHROPIC_API_KEY=sk-ant-...
export SLACK_CHANNEL_PREFIX=agent-                 # optional, default agent-
make run                                           # listens on 127.0.0.1:23001 by default
```

In another shell:

```bash
make tunnel TUNNEL_PORT=23001
```

`make tunnel` invokes `ngrok http $TUNNEL_PORT` and prints a `https://<random>.ngrok-free.app` URL (or your reserved domain if you've configured one). Copy it — this is your `SLACK_CALLBACK_URL`.

If you don't have ngrok yet:

```bash
brew install ngrok
ngrok config add-authtoken <your-token>            # from https://dashboard.ngrok.com
```

The free tier hands you a fresh random subdomain on every restart, which means you'll have to re-register it with Slack each time. To stop fighting that, reserve a static domain on the ngrok dashboard and run `ngrok http --domain=<your-domain>.ngrok.app 23001` instead of `make tunnel`.

## 4. Point Slack at your server

Pick one of the two options below. Both achieve the same thing: telling Slack to POST events to `<SLACK_CALLBACK_URL>/slack/events` and `/cortex` invocations to `<SLACK_CALLBACK_URL>/slack/commands`.

### Option A: automatic registration (recommended)

The server can push `manifest.yaml` to Slack on startup so the request URLs and event subscriptions always point at the current instance — no copying into the Slack UI. It is **opt-in** and **non-fatal**: a failure is logged and the server keeps serving. Implementation lives in `internal/slackadmin`; it runs after the listener is up so Slack's verification challenge succeeds.

Slack's `apps.manifest.update` requires an app **configuration** token — not the bot token — which expires roughly every 12 hours. Cortex manages this for you: you seed a refresh token **once**, and the server rotates it via `tooling.tokens.rotate` and persists the new pair to a `0600` state file. After the first run the state file is canonical and the seed is never read again. (See [Why seed once?](#why-seed-the-refresh-token-once) for the chain-of-trust reasoning.)

**Generate the config refresh token** at <https://api.slack.com/apps> → **Manage app configuration tokens** (bottom of the Your Apps list) — this issues an access token + refresh token pair scoped to your account. Background: <https://api.slack.com/authentication/config-tokens>. The **refresh** token is your `SLACK_CONFIG_REFRESH_TOKEN`.

Slack-specific environment variables for this path:

| Variable | When | Meaning |
|---|---|---|
| `CORTEX_SLACK_AUTOREGISTER` | enable | truthy (`1`/`true`/`yes`/`on`) turns auto-registration on; off by default |
| `SLACK_APP_ID` | required when enabled | e.g. `A0123456789` (Basic Information) |
| `SLACK_CALLBACK_URL` | required when enabled | public HTTPS base, no trailing slash; substituted for `${SLACK_CALLBACK_URL}` in the manifest |
| `SLACK_CONFIG_REFRESH_TOKEN` | first run only | app-config refresh token from api.slack.com; after first run the state file is canonical |
| `CORTEX_SLACK_TOKENS_PATH` | optional | rotating-token state file (default `$XDG_CONFIG_HOME/cortex/slack-tokens.json`) |
| `CORTEX_SLACK_MANIFEST_PATH` | optional | manifest to push (default `integrations/slack/manifest.yaml`) |

```bash
# First run: seed the refresh token once (tunnel must be up)
CORTEX_SLACK_AUTOREGISTER=true \
SLACK_APP_ID=A0123456789 \
SLACK_CALLBACK_URL=https://your-domain.ngrok-free.dev \
SLACK_CONFIG_REFRESH_TOKEN=xoxe-1-... \
SLACK_BOT_TOKEN=xoxb-... SLACK_SIGNING_SECRET=... ANTHROPIC_API_KEY=sk-ant-... \
make run

# Subsequent runs: drop SLACK_CONFIG_REFRESH_TOKEN; the state file is canonical
```

On boot you'll see `slack manifest registered ... permissions_updated=...`. If scopes changed, you'll also get a warning to reinstall the app (Slack requires human consent for new permissions). `make slack-sync` remains as a manual out-of-band alternative — see [`AUTOMATION.md`](AUTOMATION.md).

#### Why seed the refresh token once?

`tooling.tokens.rotate` only *exchanges* an existing refresh token for a fresh pair — it can't mint the first one. And the first config token can only be created by a human logged into api.slack.com, by design (the credential can rewrite your app's manifest). So the seed is the human-created first link of a chain the server then maintains on its own. Each refresh token is single-use; if you lose the state file, generate a new seed — the old value is already spent.

### Option B: manual registration

If you'd rather not enable auto-registration, set the URLs by hand in the Slack UI.

**Event Subscriptions:**

1. Open the app's **Event Subscriptions** page and toggle **Enable Events** on.
2. Set the **Request URL** to `https://<your-tunnel-host>/slack/events`.
3. Slack POSTs a signed `url_verification` payload; Cortex verifies the signature and echoes the `challenge` back (`internal/server/http.go`). You should see a green **Verified** checkmark within a couple seconds. If you don't, see the [gotchas](#common-gotchas).
4. Under **Subscribe to bot events**, add `message.channels` and save.
5. Slack will prompt you to **Reinstall App** — do it. Event subscription and scope changes only take effect after a fresh install.

**Slash command:**

1. Open the app's **Slash Commands** page and click **Create New Command**.
2. **Command**: `/cortex`
3. **Request URL**: `https://<your-tunnel-host>/slack/commands`
4. **Short description**: `Manage Cortex AI agents`
5. **Usage hint**: `agent create <name> | agent destroy <name> | agent list`
6. Leave **Escape channels, users, and links** unchecked.
7. Save. If Slack prompts you to **Reinstall App**, do it.

You should now be able to type `/cortex agent list` anywhere in the workspace and get an ephemeral reply listing active agents (initially empty).

## 5. Run the loop end-to-end

With the server running and the tunnel up, create an agent from Slack:

```
/cortex agent create demo-bot
```

Expected behaviour:

1. Cortex creates `#agent-demo-bot` and Slack returns a clickable channel mention in the slash command response.
2. Join the channel (`Cmd-K` → `agent-demo-bot` → **Join**).
3. The agent posts a hello message.
4. Type anything in the channel — if `ANTHROPIC_API_KEY` is set, the agent replies via Claude; otherwise it echoes.
5. When you're done, `/cortex agent destroy demo-bot` archives the channel.

You can also list active agents:

```
/cortex agent list
```

## Slack-related environment variables

General (non-Slack) configuration is documented in the [README](../../README.md#runtime-configuration). The Slack-specific variables are:

- `SLACK_BOT_TOKEN` (required): bot token (`xoxb-...`) carrying the scopes in [step 1](#1-create-the-slack-app). Verified via `auth.test` at boot.
- `SLACK_SIGNING_SECRET` (required): verifies Slack event and slash-command request signatures.
- `SLACK_CHANNEL_PREFIX` (optional, default `agent-`): prefix prepended to sanitized agent names when forming channel names.
- `SLACK_API_BASE_URL` (optional, default `https://slack.com/api`): Slack API base URL override, useful for tests.
- Auto-registration vars (`CORTEX_SLACK_AUTOREGISTER`, `SLACK_APP_ID`, `SLACK_CALLBACK_URL`, `SLACK_CONFIG_REFRESH_TOKEN`, `CORTEX_SLACK_TOKENS_PATH`, `CORTEX_SLACK_MANIFEST_PATH`): see [Option A](#option-a-automatic-registration-recommended).

## Common gotchas

- **`not_in_channel` posting**: the bot tried to post into a channel it isn't a member of. `channels:join` should let `ensureChannel` auto-join after a `name_taken`, but for channels created out of band you may have to invite the bot once: `/invite @cortex`.
- **`missing_scope`** during channel create: re-check the OAuth scope list and reinstall the app — adding scopes after install does not retroactively grant them.
- **Slack request URL verification fails**: confirm the cortex server is running with the real `SLACK_SIGNING_SECRET` (the challenge is signed), the tunnel is still up, the URL points at the same port as `make run`, and the path is exactly `/slack/events`.
- **Signature errors (`invalid slack request signature`)**: `SLACK_SIGNING_SECRET` is wrong, or your local clock has drifted more than 5 minutes from Slack's (`stale slack request timestamp`).
- **Auto-register fails with an `apps.manifest.update` error**: the config token is wrong or spent (re-seed `SLACK_CONFIG_REFRESH_TOKEN` from api.slack.com), `SLACK_APP_ID` is wrong, or the tunnel was down when Slack tried to verify the request URL.
- **Replies don't unblock the session**: the reply must be **in the message thread**, not a top-level message in the channel. Cortex routes by `(channel_id, thread_ts)`; top-level messages have no `thread_ts` and are ignored.
- **Bot's own messages echo back**: events with `bot_id` set are filtered out in `HumanThreadReply`, so this shouldn't bite, but worth knowing if you customize the handler.

## Production notes

For Cloud Run you'll provide `SLACK_BOT_TOKEN` and `SLACK_SIGNING_SECRET` via Secret Manager (the Terraform stack scaffolds the secrets but doesn't seed values — set them with `gcloud secrets versions add`). Set `SLACK_CALLBACK_URL` to the Cloud Run public URL; with `CORTEX_SLACK_AUTOREGISTER=true` the service registers its own URLs on deploy. You typically want a separate Slack app per environment (dev/staging/prod) so test traffic stays in a test workspace.
