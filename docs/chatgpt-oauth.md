# ChatGPT OAuth bridge

[한국어](ko/chatgpt-oauth.md)

This opt-in bridge lets a locally hosted ScopeWeaver use eligible models from a
user-authorized ChatGPT plan. It implements OpenAI's public **Sign in with ChatGPT**
flow. It does not import Codex login files, impersonate another app, use private
ChatGPT endpoints, or change ScopeWeaver's planner, workers, tasks, MCP adapter,
profile selection, or API-key providers.

The bridge connects an existing `openai-responses` profile to the official public
Responses endpoint. ScopeWeaver remains responsible for orchestration and tool
execution. This is a provider integration, not a new multi-agent scheduler. Use
the repository's existing task/agent profile bindings to assign different models
to different roles; the profile chain provides failover, not model consensus.
Existing usage restrictions still apply.

## Build and sign in

Run these commands in the same WSL instance as the Go backend:

```bash
cd /home/emathioni/scopeweaver
mkdir -p dist
go build -o dist/scopeweaver-chatgpt ./cmd/scopeweaver-chatgpt
./dist/scopeweaver-chatgpt login
```

The CLI prints a **Continue with ChatGPT** authorization URL. Open it in your
system browser, select the ChatGPT account/workspace, and authorize the app and
plan usage. The browser should return to `http://127.0.0.1:1455/auth/callback`.

**Windows + WSL:** if Windows cannot connect to that WSL loopback listener, the
browser may show a connection error after authorization. Copy the complete
callback URL from the address bar into the waiting CLI, then press Enter. The
CLI validates the same state, exact redirect URI, PKCE grant, signed ID token,
nonce, account identity and granted scopes as the HTTP callback. Do not paste
the callback or authorization URL into chat, logs, or an issue. The official
OAuth redirect must use `127.0.0.1`; changing it to `localhost` is not supported.

To avoid a busy callback port, select another available IPv4 loopback port:

```bash
./dist/scopeweaver-chatgpt login --callback-listen 127.0.0.1:54321
```

An authorization attempt times out after ten minutes. If code exchange fails,
start login again. Models, permissions, account eligibility and subscription
limits are determined by OpenAI; having a subscription does not guarantee that
this flow or a particular model is available.

## Add a profile and run the bridge

Before starting the bridge, inspect the account and its actual model catalog:

```bash
./dist/scopeweaver-chatgpt status
./dist/scopeweaver-chatgpt models
```

Choose a model's `id` from the catalog. Generate the profile fields using that
ID (replace `MODEL_ID`):

```bash
./dist/scopeweaver-chatgpt profile --model MODEL_ID
```

This prints JSON containing a separate **local bridge key**, not an OAuth token.
In ScopeWeaver's LLM settings, create a profile with those fields:

| Setting | Value |
| --- | --- |
| Format | `openai-responses` |
| Base URL | `http://127.0.0.1:8788/v1` |
| API key | `api_key` from the generated profile JSON |
| Model | Selected catalog `id` |
| Streaming | Enabled (the bridge also supports atomic non-streaming callers) |
| Maximum output tokens | `0` (the OAuth route does not accept an output cap) |

Now start the bridge and leave it running:

```bash
./dist/scopeweaver-chatgpt serve
```

Use ScopeWeaver's existing **Test connection** action, then select/bind the new
profile as usual. DeepSeek can remain active or be configured as a fallback.
Global failover must be enabled to use the global pool; explicitly bound
profiles need the existing bound-profile fallback setting. No task is created
or resumed automatically by these commands.

The bridge must run where the **backend** can reach it. Windows browser access
to the bridge is not required. In the setup above both backend and bridge are
inside WSL, so their `127.0.0.1` addresses share the same network namespace.

## Accounts, storage, and logout

Use a distinct `--account NAME` for each account/workspace registration. Pass
the same label to login, models, profile, serve, status and logout. Each record
retains its issued OAuth client ID and verified account identity; returning
sign-ins cannot replace it with another account. Different running accounts
need different `--directory` and `--listen` values. Each directory represents a
separate host and must remain stable across restarts.

Credentials default to the user's config directory under `scopeweaver/chatgpt`
(`~/.config/scopeweaver/chatgpt` on Linux). Override with `--directory PATH` only
for private local runtime storage, outside the repository. Files are written
atomically with Unix `0600` permissions inside a `0700` directory. Native
Windows builds install a protected directory ACL for the current user and
SYSTEM. Keep the directory private and out of backups/shared folders. Tokens
never enter ScopeWeaver profiles, browser storage, or the bridge HTTP API.

One process exclusively locks a credential directory, including token rotation.
Stop `serve` with Ctrl+C before running login/status/models/profile/logout.
Access tokens renew automatically near expiry; replacement refresh tokens are
persisted together with access tokens and expiry. Invalid grants require a new
login. The bridge retries an HTTP 401 once after refresh, before emitting any
inference content; it never replays a partly emitted response.

```bash
./dist/scopeweaver-chatgpt logout
```

Logout attempts remote revocation, clears local tokens, and retains the
account/client mapping and host ID for future login. If remote revocation cannot
be confirmed, the CLI reports it: disconnect the app in ChatGPT Settings.
Review app permissions and plan/credit limits in ChatGPT Settings → Usage.

Add `--lang ko` for Korean CLI instructions. A native Windows binary can be built
with `GOOS=windows GOARCH=amd64 go build -o dist/scopeweaver-chatgpt.exe
./cmd/scopeweaver-chatgpt`; use a native private directory, not the WSL credential
directory, and ensure the backend can reach the chosen bridge listener.

## Protocol and current limitations

- Only authenticated loopback HTTP clients are accepted. There is no public
  listener, CORS permission, login HTTP API, arbitrary upstream URL, or fallback
  to separately billed OpenAI API credentials.
- `/v1/models` returns account-specific visible models in a conventional
  OpenAI-compatible `data` catalog. `/v1/responses` accepts the stateless subset
  used by ScopeWeaver. `/health` is also protected by the local bridge key.
- Upstream requests always use `store:false`, `stream:true`, full history and
  namespaced local function tools. Function names and call IDs are preserved;
  replayed calls/results receive the namespace. ScopeWeaver executes the tools.
- The bridge omits `max_output_tokens` and `temperature` for this OAuth route.
  Stateful request fields and hosted tools are rejected. It supports text and
  function-call turns used by the current ScopeWeaver provider; it does not add
  image, audio, video or hosted-tool features to that provider.
- Non-streaming callers receive the final response only after a completed
  upstream stream. Truncated, failed and incomplete streams produce errors.
  Quota/auth failures before output become HTTP errors usable by the existing
  pool. Errors after partial output are surfaced without rerunning the turn.
- Claude OAuth is not implemented. Claude API-key profiles continue to work.
- This integration targets local/open-source use. Commercial/remote hosted use
  must follow OpenAI's applicable onboarding and product requirements.

## Validation

```bash
go test -race ./chatgpt ./cmd/scopeweaver-chatgpt -count=1
go vet ./chatgpt ./cmd/scopeweaver-chatgpt
```

Tests use isolated OAuth/JWKS/inference servers, signed fixture ID tokens, and
the actual ScopeWeaver provider and LLM pool. They cover identity/consent
validation, state/PKCE, returning registrations, rotation and persistence,
revocation, local access boundaries, tool history, non-streaming callers, safe
failover, and interrupted streams. A successful real login and ScopeWeaver
connection test are still required to establish access for a particular account.

Official references (checked October 13, 2026):
[overview](https://developers.openai.com/siwc/token-sharing-open-source),
[registration](https://developers.openai.com/siwc/token-sharing-open-source/sign-in),
[sessions](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions),
[inference](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference),
[preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations).
