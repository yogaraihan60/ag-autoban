# ag-autoban

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin that
auto-bans and auto-recovers **antigravity** OAuth accounts based on upstream
429 (quota exceeded) and 401/403 (invalid grant) responses.

## How it works

The plugin registers three capabilities with CLIProxyAPI:

| Capability | Method | Purpose |
|---|---|---|
| `usage_plugin` | `usage.handle` | Receives every completed request record. When an antigravity account returns 429 or 401/403, it is added to the ban table. Successful requests clear any prior ban. |
| `scheduler` | `scheduler.pick` | Before each request, CLIProxyAPI asks the plugin to pick an auth candidate. The plugin filters out candidates matching active bans and returns the highest-priority remaining candidate. Expired bans (past `reset_at`) are auto-cleared on every call. |
| `management_api` | `management.handle` | Exposes `GET /plugins/ag-autoban/status` and `POST /plugins/ag-autoban/release` for inspection and manual ban release. |

### 429 quota detection

Antigravity 429 responses include a `Resets in NhNmNs` body. The plugin parses
this to compute `reset_at` and stores it in `state.json`. The ban is
automatically released when `now > reset_at`.

### 401/403 invalid grant detection

If the response body contains `invalid_grant` or `invalid_token`, the account
is banned until its auth JSON file is replaced (detected via file mtime
change).

## State

State is persisted to a JSON file at:

```
$AG_AUTOBAN_DIR/state.json   (default: /root/.cli-proxy-api/plugins/ag-autoban/state.json)
```

The auth directory is configurable via:

```
$AG_AUTOBAN_AUTH_DIR          (default: /root/cliproxyapi/auth)
```

## Configuration

Add to `config.yaml`:

```yaml
plugins:
  enabled: true
  configs:
    ag-autoban:
      enabled: true
      priority: 100
```

Restart CLIProxyAPI after adding the config.

## Build

Requires Go 1.22+ and CGO:

```bash
./build.sh
```

Produces `ag-autoban.so` (Linux), `ag-autoban.dylib` (macOS), or
`ag-autoban.dll` (Windows).

## Install

Place the shared library under the CLIProxyAPI plugin directory:

```
plugins/linux/amd64/ag-autoban.so
```

## Management API

```bash
# View current bans
curl -H "Authorization: Bearer <key>" \
  http://localhost:8317/v0/management/plugins/ag-autoban/status

# Release all bans
curl -X POST -H "Authorization: Bearer <key>" \
  -d '{"scope":"all"}' \
  http://localhost:8317/v0/management/plugins/ag-autoban/release

# Release specific accounts
curl -X POST -H "Authorization: Bearer <key>" \
  -d '{"scope":"selected","items":["antigravity-user@gmail.com.json"]}' \
  http://localhost:8317/v0/management/plugins/ag-autoban/release
```

## License

MIT
