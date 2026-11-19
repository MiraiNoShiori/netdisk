package repository

import (
	"context"
	"github.com/google/uuid"
)

func (s *Store) SwitchBlobBackend(ctx context.Context, id uuid.UUID, oldBackend, oldKey, backend, key string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE blobs SET backend=$4, storage_key=$5 WHERE id=$1 AND backend=$2 AND storage_key=$3`, id, oldBackend, oldKey, backend, key)
	return tag.RowsAffected() == 1, translate(err)
}
func (s *Store) ReclaimOrphanBlobs(ctx context.Context, owner, root uuid.UUID) ([]string, error) {
	locs, err := s.ReclaimOrphanLocations(ctx, owner, root)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(locs))
	for _, v := range locs {
		keys = append(keys, v.Key)
	}
	return keys, nil
}
