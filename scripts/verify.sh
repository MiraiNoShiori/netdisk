#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
export NETDISK_DATABASE_URL="${NETDISK_DATABASE_URL:-postgres://netdisk:netdisk@localhost:15433/netdisk_test?sslmode=disable}"
export NETDISK_TEST_DATABASE_URL="$NETDISK_DATABASE_URL"
export NETDISK_TEST_S3_ENDPOINT="${NETDISK_TEST_S3_ENDPOINT:-localhost:19000}"
export NETDISK_JWT_SECRET="${NETDISK_JWT_SECRET:-verification-only-key}"
export NETDISK_REQUIRE_INTEGRATION=1
go run ./cmd/migrate
test -z "$(gofmt -l .)"
go vet ./...
go test -race ./... -count=1 -timeout=10m
go build ./cmd/...
