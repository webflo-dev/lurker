# lurker

A self-hosted, read-only Reddit reader — a Go rewrite of
[oppiliappan/lurker](https://github.com/oppiliappan/lurker) with plain
HTML templates and OpenID Connect authentication.

- server-rendered `html/template` pages, zero client-side JavaScript
- renders well on mobile, respects `prefers-color-scheme`
- subscribe to subreddits without a Reddit account
- comment collapsing, single-thread view, post & subreddit search
- NSFW/spoiler thumbnails blurred by default
- login through any OpenID Connect provider (Keycloak, Authelia,
  Authentik, Dex, Auth0, …) — no invite system, your IdP decides who
  gets in
- subscriptions stored in [bbolt](https://github.com/etcd-io/bbolt), an
  embedded pure-Go key/value store: a single file, nothing else to run
- minimal dependencies: bbolt is the only module outside the standard
  library (OIDC, sessions and templating are all stdlib)

## Configuration

Everything is configured through environment variables:

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `LURKER_PORT` | no | `3000` | HTTP listening port |
| `LURKER_BASE_URL` | recommended | `http://localhost:<port>` | Public URL of the instance; used for the OIDC redirect URI and secure cookies |
| `LURKER_DB_PATH` | no | `./lurker.db` | Path of the bbolt database file |
| `LURKER_SESSION_SECRET` | recommended | random | Secret (≥ 32 chars) signing session cookies; without it sessions reset on restart |
| `LURKER_OIDC_DISABLED` | no | `false` | Disable authentication entirely (see below) |
| `LURKER_OIDC_ISSUER` | **yes*** | — | OIDC issuer URL, e.g. `https://auth.example.com/realms/main` |
| `LURKER_OIDC_CLIENT_ID` | **yes*** | — | OIDC client ID |
| `LURKER_OIDC_CLIENT_SECRET` | **yes*** | — | OIDC client secret (confidential client) |
| `LURKER_OIDC_SCOPES` | no | `openid profile email` | Space-separated scopes |
| `LURKER_USER_AGENT` | no | Chrome-like | User-Agent sent to the Reddit API |
| `LURKER_REDDIT_URL` | no | `https://www.reddit.com` | Base URL of the Reddit JSON API (mirror or proxy) |

\* not required when `LURKER_OIDC_DISABLED=true`.

In your identity provider, register a confidential client with the
redirect URI `<LURKER_BASE_URL>/oidc/callback`. ID tokens signed with
RS256 or ES256 are supported, and provider metadata is discovered from
`<issuer>/.well-known/openid-configuration`.

### Running without authentication

Set `LURKER_OIDC_DISABLED=true` to turn authentication off: no login
page, and every visitor browses (and shares subscriptions) as a single
local user. Only do this for personal/local instances or behind another
authentication layer such as a VPN or an authenticating reverse proxy.

## Running

```sh
export LURKER_BASE_URL=https://lurker.example.com
export LURKER_OIDC_ISSUER=https://auth.example.com/realms/main
export LURKER_OIDC_CLIENT_ID=lurker
export LURKER_OIDC_CLIENT_SECRET=…
export LURKER_SESSION_SECRET=$(head -c 32 /dev/urandom | base64)

go run .            # or: go build && ./lurker
```

### Docker

```sh
docker build -t lurker .
docker run -p 3000:3000 -v lurker-data:/data \
  -e LURKER_BASE_URL=https://lurker.example.com \
  -e LURKER_OIDC_ISSUER=https://auth.example.com/realms/main \
  -e LURKER_OIDC_CLIENT_ID=lurker \
  -e LURKER_OIDC_CLIENT_SECRET=… \
  -e LURKER_SESSION_SECRET=… \
  lurker
```

The image runs as a non-root user and stores its database in `/data`.

## Troubleshooting

**`reddit: … returned 403 Forbidden`** — Reddit's CDN refuses clients
it doesn't recognize as browsers, and blocks many datacenter/VPN IP
ranges outright. lurker already sends browser-like headers; if you
still get 403s, try a different `LURKER_USER_AGENT` (copy your
browser's exact string), or host the instance on an IP Reddit accepts,
or point `LURKER_REDDIT_URL` at a proxy/mirror you trust.

## Development

```sh
go test ./...
```

The test suite spins up an in-process fake OIDC provider and a fake
Reddit API, so it runs fully offline.

## Routes

| Route | Description |
| --- | --- |
| `/` | Combined feed of your subscriptions (or `r/all`) |
| `/r/<sub>` | Subreddit listing — `?sort=hot\|new\|top\|rising\|controversial`, `&t=day\|week\|…` |
| `/comments/<id>` | Post with its comment tree |
| `/comments/<id>/comment/<cid>` | Single comment thread |
| `/subs` | Manage subscriptions |
| `/search`, `/post-search`, `/sub-search` | Search posts and subreddits |
| `/media` | Image/video viewer for Reddit-hosted media |
| `/login`, `/logout`, `/oidc/*` | Authentication |
| `/healthz` | Liveness probe |
