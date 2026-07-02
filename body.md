## Upstream sync

This PR brings `12` new commits from `kenn-io/agentsview:main` into this fork.

### Commits

- 811dd8a fix(desktop): validate updater signatures (#931)
- af84ff8 docs: 0.35.0 release notes, accuracy fixes, and new-feature screenshots (#926)
- 438e13c Reuse DuckDB path for Quack sync (#930)
- b7cc70d feat(cli): hint that Copilot usage records no per-message tokens (#349) (#934)
- 4f240b3 fix(desktop): surface backend startup failures (#932)
- 6911729 feat(parser): support OpenClaude sessions (#935)
- 9defe23 fix(cli): require daemon transport for read commands (#933)
- 39ee5c7 docs: update 0.35.1 release notes (#938)
- 307568e fix(postgres): don't let skipped ownership conflicts block the session-alias backfill marker (#940)
- 5bd246f docs: add 0.35.2 changelog (#941)
- c94d4be feat(frontend): add Traditional Chinese (zh-TW) localization (#937)
- 9d4e188 Improve Windows test suite fixture reuse (#943)

### Review checklist

- [ ] Local tests pass (`make test-short`)
- [ ] Build succeeds on Apple Silicon (`make build-local-apple-silicon`)
- [ ] UI smoke check after merge
