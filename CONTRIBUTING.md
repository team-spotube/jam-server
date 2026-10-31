# Contributing

Thanks for helping with the Spotube Jam server. It's small on purpose: a relay with
memory for one room, serving the clock everyone syncs to. Most Jam features live in the
[Spotube app](https://github.com/team-spotube/spotube), so check whether your change
belongs there first.

## Before you start

- For anything bigger than a small fix, open an issue first so we can agree on the
  approach.
- Security problems go through [SECURITY.md](SECURITY.md), never a public issue.
- By contributing, you agree that your contribution is licensed under the
  [AGPL-3.0-or-later](LICENSE), like the rest of the project.

## Setup

Needs Node.js 22 or later and git.

```sh
pnpm install      # dependencies, plus the git hooks (lefthook)
pnpm dev      # local server on http://127.0.0.1:8787, no Cloudflare account needed
pnpm test         # in another terminal: end-to-end checks against the dev server
```

`pnpm install` sets up a pre-commit hook that adds the license header to new files and
typechecks. If hooks didn't install (for example, you ran `pnpm install` before
`git init`), run `pnpm exec lefthook install`.

## Making a change

1. Create a branch from `main`.
2. Make your change. Keep these in mind:
   - **Cost**: every incoming WebSocket message and every storage write is billed.
     Don't add periodic frames, polling or writes on the steady path. Keep-alives must
     stay runtime auto-responses.
   - **Hibernation**: rooms sleep between messages, and in-memory state is lost when
     they do. Anything that must survive goes in `ctx.storage` or a socket attachment.
   - **Trust**: the Worker validates everything from clients. Never trust a client's
     claims about who it is; use the id the Worker derives.
   - **Privacy**: never store or log raw device details or IP addresses. Only hashed,
     per-room values (see `deviceMark`).
3. Add or update a step in `test/smoke.mjs` for any behavior change, including what
   should be refused.
4. Check everything passes:

   ```sh
   pnpm typecheck
   pnpm license:check
   pnpm test          # with `pnpm dev` running
   ```

5. Open a pull request using the template.

## Protocol changes

The wire protocol is shared with the app (`core/jam/JamRoomClient.kt` in Spotube).

- **Compatible changes** (a new optional field, a new frame old clients can ignore): fine
  on their own. Mention them in the pull request.
- **Breaking changes**: bump `PROTOCOL_VERSION` in `src/protocol.ts`, and open the
  matching change in the Spotube repository. Old app versions get a "server runs an
  incompatible version" error, so we release both together.

## Style

- TypeScript in strict mode; `pnpm typecheck` must pass.
- 2-space indentation (see `.editorconfig`).
- Comment the why, not the what, and keep comments short.
- Every source file starts with the AGPL header from `.github/agpl_header.txt`. The hook
  adds it; `pnpm license:fix` adds it to every tracked file.

## Commit messages

[Conventional Commits](https://www.conventionalcommits.org/), as in Spotube:
`feat: …`, `fix: …`, `docs: …`, `refactor: …`, `test: …`, `chore: …`.

## Deployments

Maintainers deploy from `main` with the `Deploy` workflow. Self-hosting is described in
the [README](README.md#self-host).
