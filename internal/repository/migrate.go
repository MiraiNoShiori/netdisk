package repository

import (
	"context"
	"fmt"
	"github.com/Miraitowa-kawayi/netdisk/migrations"
	"sort"
)

// Migrate 可重复运行；对旧库接管 0001，后续迁移逐个事务执行，不删除用户数据。
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock(831027114)`); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(831027114)`)
	if _, err = conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		name := entry.Name()
		var done bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		var exists bool
		if name == "0001_init.sql" {
			err = tx.QueryRow(ctx, `SELECT to_regclass('users') IS NOT NULL`).Scan(&exists)
		}
		if err == nil && !exists {
			var sql []byte
			sql, err = migrations.Files.ReadFile(name)
			if err == nil {
				_, err = tx.Exec(ctx, string(sql))
			}
		}
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, name)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
