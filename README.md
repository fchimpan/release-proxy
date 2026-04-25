# release-proxy

A Go module proxy that **blocks recently-published versions** for a configurable cooldown
period. It sits between `go` and the upstream proxy (defaults to `proxy.golang.org`)
and hides versions that are too fresh from `go get` / `go mod tidy`, giving the community
time to discover supply-chain compromises or regressions before they propagate into your
build.

Inspired by pnpm's [`minimumReleaseAge`](https://pnpm.io/settings#minimumreleaseage).

## Why

- Compromised npm packages and accidental bad releases show that **the newest version is the riskiest**.
- Delaying every dependency update by a small window lets vulnerability scanners,
  the community, and the maintainer themselves catch problems before you ship them.
- The Go toolchain has no built-in equivalent (discussed upstream in
  [golang/go#76485](https://github.com/golang/go/issues/76485)), so release-proxy
  implements the policy as a transparent HTTP proxy in front of `proxy.golang.org`.

## How it works

When a `go` client fetches a module via `GOPROXY`, release-proxy resolves each version's
publication time from the upstream `.info` endpoint and filters anything younger than the
configured cooldown.

| Endpoint | Behaviour |
|---|---|
| `/@v/list` | Returns the version list with cooldown-bound versions removed |
| `/@latest` | Returns 404 if the latest version is within the cooldown window |
| `/@v/{version}.{info,mod,zip}` | Returns 404 if the pinned version is within cooldown |
| `/sumdb/...` | Returns 404 so `go` falls back to `sum.golang.org` directly |
| `/healthz` | Liveness probe |

## Install

```sh
go install github.com/fchimpan/release-proxy@latest
```

Build locally:

```sh
make build           # ./bin/release-proxy
make docker-build    # release-proxy:latest image
```

## Docker

Multi-arch images (`linux/amd64`, `linux/arm64`) are published to GitHub Container
Registry. The image is built from a `distroless/static:nonroot` base and runs as an
unprivileged user.

| Tag pattern | Source |
|---|---|
| `0.0.1`, `0.0`, `0`, `latest` | tagged release (semver) |
| `main` | rolling build of the `main` branch |
| `sha-XXXXXXX` | immutable per-commit |

```sh
docker pull ghcr.io/fchimpan/release-proxy:latest

docker run --rm -p 8080:8080 \
  -e RELEASE_PROXY_MINIMUM_RELEASE_AGE=7d \
  ghcr.io/fchimpan/release-proxy:latest
```

Mounting a config file (the default path inside the container is `/release-proxy.json`):

```sh
docker run --rm -p 8080:8080 \
  -v "$PWD/release-proxy.json:/release-proxy.json:ro" \
  ghcr.io/fchimpan/release-proxy:latest
```

`docker-compose.yml`:

```yaml
services:
  release-proxy:
    image: ghcr.io/fchimpan/release-proxy:latest
    ports:
      - "8080:8080"
    volumes:
      - ./release-proxy.json:/release-proxy.json:ro
    restart: unless-stopped
```

## Quick start

```sh
# Run with a 7-day cooldown
RELEASE_PROXY_MINIMUM_RELEASE_AGE=7d ./bin/release-proxy &

# Use it as GOPROXY
mkdir /tmp/demo && cd /tmp/demo
go mod init demo
GOPROXY=http://localhost:8080 go get golang.org/x/text
```

## Configuration

Settings are resolved in the order **env var > config file > built-in default**.

### Config file (`release-proxy.json`)

If `release-proxy.json` exists in the current working directory it is loaded
automatically. Use `RELEASE_PROXY_CONFIG=/path/to/config.json` to point elsewhere.

```json
{
  "minimum-release-age": "7d",
  "minimum-release-age-exclude": [
    "github.com/mycompany/",
    "internal.corp/"
  ],
  "upstream": "https://proxy.golang.org",
  "port": "8080",
  "log-level": "info",
  "cache-ttl": "1h"
}
```

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `RELEASE_PROXY_CONFIG` | `release-proxy.json` | Path to the JSON config file |
| `RELEASE_PROXY_MINIMUM_RELEASE_AGE` | (unset) | Default cooldown when the URL has no prefix |
| `RELEASE_PROXY_MINIMUM_RELEASE_AGE_EXCLUDE` | (unset) | Comma-separated module prefixes to bypass filtering |
| `RELEASE_PROXY_UPSTREAM` | `https://proxy.golang.org` | Upstream Go module proxy |
| `RELEASE_PROXY_PORT` | `8080` | Listen port |
| `RELEASE_PROXY_CACHE_TTL` | `1h` | TTL for the in-memory `.info` cache |
| `RELEASE_PROXY_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

### Duration format

`30m`, `24h`, `7d`, `2w` (single unit only). Compound forms like `1d12h` are not
supported.

## URL format

You can also specify the cooldown per-request via the URL prefix:

```
GET /{cooldown}/{module-path}/{endpoint}
```

Examples:

```sh
curl http://localhost:8080/7d/golang.org/x/text/@v/list
curl http://localhost:8080/7d/golang.org/x/text/@latest
curl http://localhost:8080/30d/golang.org/x/text/@v/v0.3.0.info
```

The Go toolchain doesn't emit such prefixes, so when used as `GOPROXY` you **must set
`RELEASE_PROXY_MINIMUM_RELEASE_AGE`** (or the equivalent config key).

## Using as `GOPROXY`

```sh
RELEASE_PROXY_MINIMUM_RELEASE_AGE=7d ./bin/release-proxy &

# No fallback — proxy is mandatory
GOPROXY=http://localhost:8080 go mod tidy

# With fallback — proxy.golang.org is consulted on 404, which defeats the cooldown.
# Not recommended.
GOPROXY=http://localhost:8080,https://proxy.golang.org go mod tidy
```

`GOSUMDB=off` is **not** required. release-proxy returns 404 for `/sumdb/...`, which
makes `go` talk to `sum.golang.org` directly for checksum verification.

## Applying urgent security patches

When a CVE is fixed in a fresh release that the cooldown would otherwise hide:

**A. One-shot bypass** — temporarily skip the proxy
```sh
GOPROXY=https://proxy.golang.org go get golang.org/x/text@v0.36.1
```

**B. Persistent allow-list** — add the module to `minimum-release-age-exclude`
```json
{
  "minimum-release-age-exclude": ["golang.org/x/text/"]
}
```

**C. Lower the global cooldown** — set the env var for the duration of the update
```sh
RELEASE_PROXY_MINIMUM_RELEASE_AGE=0s ./bin/release-proxy
```

## Excluded modules

Modules whose path matches an entry in `minimum-release-age-exclude` are passed through
to the upstream without any filtering. Typical use cases:

- Internal modules where you trust your own release process
- Core libraries you've vetted and want to receive without delay

```json
{
  "minimum-release-age-exclude": [
    "github.com/mycompany/",
    "internal.corp/"
  ]
}
```

## License

MIT
