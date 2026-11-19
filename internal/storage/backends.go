package storage

import (
	"context"
	"fmt"
	"io"
)

// Backends 保留所有已配置后端；默认值仅影响新内容，旧内容按数据库 backend 读取。
type Backends struct {
	Storage
	Default string
	Stores  map[string]Storage
}

func (b *Backends) For(name string) (Storage, error) {
	st, ok := b.Stores[name]
	if !ok {
		return nil, fmt.Errorf("storage backend %q is not configured", name)
	}
	return st, nil
}
func For(st Storage, name string) (Storage, error) {
	if b, ok := st.(*Backends); ok {
		return b.For(name)
	}
	return st, nil
}
func OpenFrom(ctx context.Context, st Storage, backend, key string) (io.ReadSeekCloser, ObjectInfo, error) {
	s, err := For(st, backend)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	return s.Open(ctx, key)
}
