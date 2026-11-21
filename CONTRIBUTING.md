# Contributing to NetDisk

Thanks for your interest in contributing! Whether it's a bug fix, a missing test, a doc tweak, or a small feature, pull requests are welcome.

## Development Setup

```sh
# 1. Start PostgreSQL (and optionally MinIO)
docker compose up -d postgres

# 2. Copy default config
cp .env.example .env

# 3. Run the server locally
go run ./cmd/server
```

## Project Conventions

- **Keep layers unidirectional**: `handler -> service -> repository / storage`. Business logic lives in `service`, SQL stays in `repository`, HTTP status mapping stays in `handler`.
- **Stream all file I/O**: Never buffer uploaded/downloaded bodies or ZIP archives into memory (`io.ReadAll` is forbidden on file content paths).
- **Backward-compatible migrations**: Add new SQL files sequentially under `migrations/` and make them idempotent where possible.
- **Formatting & verification**: Before opening a PR, run:

```sh
gofmt -w .
go vet ./...
go test -count=1 ./...
```

For full integration tests (real PostgreSQL, MinIO, and NFS TCP RPC with `-race`):

```sh
docker compose -p netdisk-test -f compose.test.yml up -d --build --wait
sh scripts/verify.sh
docker compose -p netdisk-test -f compose.test.yml down
```

## Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`).
