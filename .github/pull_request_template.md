## What and why

<!-- What does this change, and why? Link the issue if there is one. -->

## Checklist

- [ ] `npm run typecheck` passes
- [ ] `npm test` passes against `npm run dev`, with a smoke test step for the new behavior
- [ ] No new periodic frames, polling or storage writes on the steady path (cost)
- [ ] Protocol: compatible, or `PROTOCOL_VERSION` bumped with a matching Spotube change
- [ ] Nothing new stores or logs raw device details or IP addresses
