package storage

import (
	"bytes"
	"context"
	"fmt"
	"testing"
)

func BenchmarkLocalPutStream4MiB(b *testing.B) {
	loc, err := NewLocal(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	payload := bytes.Repeat([]byte("netdisk-stream-bench-"), (4<<20)/21+1)[:4<<20]
	ctx := context.Background()

	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := loc.Put(ctx, "bench/put-target", bytes.NewReader(payload), int64(len(payload))); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	_ = loc.Delete(ctx, "bench/put-target")
}

func BenchmarkLocalConcat4x1MiB(b *testing.B) {
	loc, err := NewLocal(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	chunk := bytes.Repeat([]byte("chunk-data-"), (1<<20)/11+1)[:1<<20]
	parts := make([]string, 4)
	for i := range parts {
		parts[i] = fmt.Sprintf("bench/part-%d", i)
		if _, err := loc.Put(ctx, parts[i], bytes.NewReader(chunk), int64(len(chunk))); err != nil {
			b.Fatal(err)
		}
	}

	b.SetBytes(int64(len(chunk) * len(parts)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := loc.Concat(ctx, "bench/concat-target", parts); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	_ = loc.Delete(ctx, "bench/concat-target")
}
