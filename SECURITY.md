# Security policy

## Reporting a vulnerability

Please don't open a public issue for security problems. Report them privately through
GitHub: **Security → Report a vulnerability** on this repository.

Include what you found, how to reproduce it, and what an attacker could do with it. We'll
confirm we received it, keep you updated, and credit you in the fix unless you'd rather
stay anonymous.

## Supported versions

Only the latest `main` (what runs on Spotube's public server) gets fixes. If you
self-host, update to the latest `main`.

## Scope

In scope:

- Taking over a room, acting as another participant, or bypassing host-only actions.
- Bypassing bans, rate limits or message budgets.
- Learning something about other participants that the server shouldn't reveal (IP
  addresses, device details, activity in other rooms).
- Making the server cost money or stop serving others out of proportion to your effort.

Known limits, documented in the [README](README.md#security), are not vulnerabilities
by themselves: anyone with a room code can join, and a banned person can get back in
from a different device or network.

The Spotube app has its own policy in the
[Spotube repository](https://github.com/team-spotube/spotube).
