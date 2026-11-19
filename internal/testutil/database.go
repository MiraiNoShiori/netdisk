// Package testutil 给每个集成测试独立 schema，避免内容哈希跨临时存储目录复用。
package testutil

import (
	"context"
	"github.com/Miraitowa-kawayi/netdisk/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"net/url"
	"os"
	"strings"
	"testing"
)

func Database(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("NETDISK_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("NETDISK_REQUIRE_INTEGRATION") == "1" {
			t.Fatal("NETDISK_TEST_DATABASE_URL required")
		}
		t.Skip("NETDISK_TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = conn.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); conn.Close(ctx) })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	isolated := u.String()
	store, err := repository.New(ctx, isolated)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Migrate(ctx)
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	return isolated
}
