# Spotube Jam server

[![CI](https://github.com/team-spotube/jam-server/actions/workflows/ci.yml/badge.svg)](https://github.com/team-spotube/jam-server/actions/workflows/ci.yml)
[![License: AGPL-3.0-or-later](https://img.shields.io/badge/license-AGPL--3.0--or--later-blue.svg)](LICENSE)

The room server behind [Spotube](https://github.com/team-spotube/spotube)'s Group Jam, where
friends listen to the same music at the same moment on their own devices. It's a
Cloudflare Worker with one
[Durable Object](https://developers.cloudflare.com/durable-objects/) per room code.

## How it works

Each room relays a few small JSON frames between the host and its guests. Nobody streams
audio through it (every device fetches its own), so a room costs a few KB per minute.

### Cost

Rooms are billed per incoming message (20 count as one request) and per storage write,
so steady playback sends almost nothing:

- The host sends a playback anchor only when playback changes (play/pause, seek, skip,
  speed), when its player drifts from its last anchor, and once a minute as a safety net.
  Guests extrapolate anchors with the shared clock in between.
- Anchors are kept in memory, not storage. A room that hibernated wakes for a join, and
  the host answers the join with a fresh anchor. Only queue changes and membership
  bookkeeping are written.
- Keep-alives (`"ping"` → `"pong"`) are answered by the runtime without waking the room.
  Clock pings go out on join and every 10 minutes.

A four-person room sends roughly 200 messages an hour (about 10 billed requests).

### What a room does

- **Membership**: tracks who is connected, who hosts, and who is banned. If the host
  drops, it keeps hosting for 30 s (so it can reconnect, e.g. after switching networks), then the longest-connected guest
  takes over.
- **Retained state**: keeps the host's latest queue (and, while awake, its playback
  anchor) so late joiners start in sync. An empty room keeps them for 10 minutes, then deletes itself.
- **Routing**: guest commands (play/pause, skip, seek, suggestions) go to the host.
  Kicks and bans are enforced by the server.
- **Clock**: answers pings with its own time. Every device syncs playback to this one
  clock (see `JamClock` in the app).

The wire protocol is in [`src/protocol.ts`](src/protocol.ts). The app side lives in the
Spotube repository under
[`composeApp/src/commonMain/kotlin/org/spotube/spotube/core/jam/`](https://github.com/team-spotube/spotube/tree/master/composeApp/src/commonMain/kotlin/org/spotube/spotube/core/jam).
The two must agree: `PROTOCOL_VERSION` is bumped on both sides for breaking changes.

### Project layout

| Path | What it is |
|---|---|
| `src/index.ts` | The Worker: health check, input validation, rate limit, routing to a room |
| `src/room.ts` | `JamRoom`, the Durable Object for one room |
| `src/protocol.ts` | Wire frames, limits, error codes and id/hash helpers |
| `test/smoke.mjs` | End-to-end checks against a running server, including attacks |
| `scripts/license-header.mjs` | Adds and checks the AGPL header |

## Security

To report a vulnerability, see [SECURITY.md](SECURITY.md).

Anyone who knows a room code can join it, as with any party-code app. Everything else
is enforced here, not in the app:

- **Identity**: each install keeps a random secret, and its key for a room is derived
  from that secret and the room code. Everyone else only sees an id derived from the key
  (`participantId`, different in every room). Knowing someone's id, the host's included,
  doesn't let you connect as them, so nobody can take over hosting, kick people or replace
  someone's connection.
- **Host powers**: only the current host can set the queue and anchor or kick and ban.
  Guests can only send commands, and those go to the host, which decides what to apply.
- **Taking over rooms**: "create" only works on a fresh code, or for the room's own host
  bringing back a room that expired while everyone was offline.
- **Abuse and cost**: connects are rate-limited per IP (60 a minute; also slows down code
  guessing). Each socket has a message budget and a queue budget (`FRAME_BUDGET`,
  `QUEUE_BUDGET`), and a client that floods is disconnected with `rate_limited`.
  Frames over 512 KB are refused, and names are cleaned of control characters.
- **What the Spotube app does with peer data**: queue items and suggestions from other devices
  are cleaned before use (`JamMediaItem.sanitized`): streaming tracks only, no URLs, only
  https covers. The host credits suggestions to the sender the server reports, not to
  whatever name the sender claims. The host's local files go out as an opaque id, never
  as a path.
- **Privacy**: participants never see each other's IP addresses, since everything goes
  through the room. The room keeps no history: the queue and bans are deleted 10 minutes
  after the last person leaves.

Known limits:

- **Bans**: a ban covers the identity (the same install gets the same identity in a
  room every time) and the device. The app sends a digest of stable device traits
  (model, OS version, locale, …) hashed with the room code. The server hashes that again
  with the network (IPv4 address or IPv6 /64), so the room only stores an opaque value
  that means nothing outside it. Someone who reinstalls and rejoins from the same device
  and network is refused. People who were already in the room aren't affected, so a
  friend on the same Wi-Fi with the same phone is never caught by someone else's ban.
  Ways around it: a different device or network, or a modified client. A new arrival
  with exactly the same phone, OS version and settings on the banned person's network
  would also be refused.
- **Room codes**: six characters, about a billion codes. That's fine against guessing
  at the rate limit, but a code someone sees on your screen lets them in.
- **Spending**: on the paid plan, a determined attacker with many IPs can still run up
  usage. Set a usage alert in the Cloudflare dashboard. The free plan just stops at its
  daily limit (00:00 UTC reset) instead of billing.

## Self-host

[`src-go/`](src-go) is the same server as a single Go binary with SQLite instead of
Durable Objects, for running on your own VM. It speaks the same protocol (the smoke test
passes against both), so Spotube only needs its URL in Settings → Jam → Server URL.

```sh
pnpm go:build                  # builds bin/spotube-jamd
JAM_ADDR=:8787 JAM_DB=/var/lib/jam/jam.db bin/spotube-jamd
# or run without building: pnpm go:run (keeps its database in .data/)
# or: pnpm go:docker && docker run -p 8787:8787 -v jam:/data spotube-jamd
```

| Variable        | Default   | Meaning                                                                 |
| --------------- | --------- | ----------------------------------------------------------------------- |
| `JAM_ADDR`      | `:8787`   | Listen address.                                                         |
| `JAM_DB`        | `jam.db`  | SQLite file (queues, members, bans; kept across restarts).              |
| `JAM_IP_HEADER` | unset     | Header carrying the client IP behind a proxy (`X-Forwarded-For`, …).    |

Notes:

- Phones need HTTPS: put it behind a reverse proxy that terminates TLS and forwards
  WebSocket upgrades (Caddy and nginx both do). Set `JAM_IP_HEADER` only if the proxy
  overwrites that header, otherwise clients can spoof their IP and dodge the rate limit
  and device bans.
- It is one process: rooms live in its memory and aren't shared between instances.
- The connect rate limit (60 per minute per IP) is exact here, not per location.

## Develop

Needs Node.js 22 or later. No Cloudflare account is needed: `wrangler dev` runs the
Worker and its Durable Objects locally.

```sh
pnpm install          # also installs the git hooks (lefthook)
pnpm dev          # http://127.0.0.1:8787
pnpm test             # smoke test against the dev server (another terminal)
pnpm typecheck
pnpm go:test        # the self-hosted server (also go:run, go:build, go:lint, go:fmt, go:docker)
```

To run Spotube's own end-to-end tests against it (sync simulation, network loss), run
this in a Spotube checkout:

```sh
JAM_TEST_URL=http://127.0.0.1:8787 ./gradlew :composeApp:jvmTest --tests 'org.spotube.spotube.core.jam.*'
```

To try the real app against it, enter `http://127.0.0.1:8787` in Spotube → Settings →
Jam → Server URL. Phones need HTTPS: expose the dev server with
`pnpm dlx cloudflared tunnel --url http://localhost:8787` and use the URL it prints.

See [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.

## Deploy

```sh
CLOUDFLARE_API_TOKEN=... CLOUDFLARE_ACCOUNT_ID=... pnpm exec wrangler deploy
```

The token needs the "Edit Cloudflare Workers" permissions. Durable Objects on the
SQLite backend work on the free plan.

`.github/workflows/deploy.yml` deploys every push to `main` (it runs in the `production`
environment, so you can require approval there). It needs the repository secrets
`CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID`; the optional variable `JAM_SERVER_URL`
runs the smoke test against the deployment afterwards.

## Self-host

Deploy to your own Cloudflare account as above, then enter the Worker's URL in
Spotube → Settings → Jam → Server URL.

## Limits

- 32 participants per room, 512 KB per frame.
- 60 room connections per minute per IP (`CONNECT_LIMITER` in `wrangler.jsonc`).

## License

Copyright (C) 2026 Kingkor Roy Tirtho and Spotube Contributors.

[GNU Affero General Public License v3.0 or later](LICENSE). If you run a modified version
of this server for others, the AGPL requires you to offer them its source code.
