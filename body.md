## Upstream sync

This PR brings `17` new commits from `kenn-io/agentsview:main` into this fork.

### Commits

- 78da66df feat(export): add combined project, model and agent reporting (#1713)
- c55d650e test(parser): canonicalize temp paths in Codex fork capture tests (#1755)
- 28b3f638 fix(transcript): keep filtered code blocks expandable (#1754)
- a15e06e9 fix(parser): preserve OpenCode v2 tool-result file payloads (#1647)
- ad850d90 refactor(goose): reuse shared SQLite provider infrastructure (#1746)
- 2931ed22 Include complete project identity in reporting exports (#1758)
- 91942b3a fix(sync): offload tool-result images at ingest (#1729)
- 83f462d2 fix(usage): show preparation progress and preserve session freshness (#1756)
- 290f5101 refactor(parser): share SQLite source handling (#1747)
- 9f9dfecc feat(frontend): copy resume commands for remote sessions (#1748) (#1760)
- d6138296 fix(claude): honor ai-title for Claude sessions (#1761)
- f64cb715 fix(search): make in-session search reliable (#1743)
- 0c95d1a9 perf(parser): reuse git root resolution within sync passes (#1759)
- 9be7745a fix(deps): update go dependencies (#1762)
- 9899c3a0 fix(kilo): import projections without a sequence column (#1772)
- d20e8417 fix(sync): replicate archived sessions despite ingestion failures (#1771)
- ff4eab12 feat(frontend): separate provider and UUID path segments in session links (#1769)

### Review checklist

- [ ] Local tests pass (`make test-short`)
- [ ] Build succeeds on Apple Silicon (`make build-local-apple-silicon`)
- [ ] UI smoke check after merge
