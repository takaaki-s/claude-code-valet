**English** | [日本語](CONTRIBUTING.ja.md)

# Contributing

Thanks for your interest in jind-ai.

Please read [Project status](README.md#project-status) first. This is a personal
project maintained by one developer in their spare time, and that shapes
everything below.

## Response expectations

- There is no response-time guarantee for issues or pull requests.
- Silence usually means the maintainer has not had time yet, not rejection.
- An issue or pull request may be closed as out of scope. That is a statement
  about this project's boundaries, not about the merit of the idea.

Setting these expectations up front is intended to be honest rather than
discouraging.

## What is welcome

- **Bug reports** with reproduction steps. These are the single most useful
  contribution.
- **Bug fixes**, ideally with a regression test.
- **Documentation fixes** — typos, broken links, unclear wording.
- **Tests** covering existing behaviour.

Small, focused pull requests are far more likely to be merged than large ones.

## Please open an issue first

For the following, start a discussion before writing code. A pull request sent
without prior discussion may be declined even when the code itself is good.

- **New features** and TUI changes.
- **New agent adapters.** Each adapter tracks an external CLI that changes
  independently of this project, so every adapter is a standing maintenance
  commitment rather than a one-off contribution. New adapters are accepted only
  when there is a realistic plan for keeping them working.
- **Refactors spanning more than one package.**
- **New dependencies.** The dependency tree is kept deliberately small, and
  tests use the standard library only.

## Out of scope

- **Windows.** Released builds target Linux and macOS.
- **Non-tmux backends.** tmux is a core architectural assumption, not an
  implementation detail that can be swapped out.

## Development

Requires Go 1.26 and tmux.

```
make build       # → bin/jin
make test        # go test -v ./...
make test-race   # go test -race ./...
make fmt         # go fmt ./...
make lint        # golangci-lint run ./...
```

Run `make fmt`, `make lint`, and `make test-race` before opening a pull request.
CI runs lint and the race-enabled test suite; both must pass.

Add `_test.go` coverage for new code. Tests use the standard library only — no
assertion libraries.

For orientation, see [docs/architecture.md](docs/architecture.md) and
[docs/conventions.md](docs/conventions.md).

## Commit messages

This project follows [Conventional Commits](https://www.conventionalcommits.org/).
Release changelogs are generated from commit messages, so the prefix matters:

`feat:` / `fix:` / `refactor:` / `docs:` / `test:` / `chore:`

## License

By contributing, you agree that your contributions are licensed under the
MIT License.
