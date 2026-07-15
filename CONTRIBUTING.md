# Contributing to ike

Thanks for your interest! ike is early and small — process is deliberately
minimal.

1. **Read [AGENTS.md](AGENTS.md).** It is the contributor guide: build/test
   commands, layering rules, testing patterns, and the hard rules (notably:
   no secrets in the config file, read-only API surface, everything must
   work in `--demo`).
2. **Open an issue before large changes.** Small fixes can go straight to a
   PR.
3. **Every PR needs:** `gofmt -l .` clean, `go vet ./...` clean,
   `go test ./...` green, a SimulationScreen e2e test for any new
   interaction, and doc updates (README / docs/) in the same PR.
4. **UX questions default to k9s behavior.** If k9s does it some way, ike
   does too unless docs/DESIGN.md records a reason not to.

By contributing you agree your work is licensed under Apache-2.0.
