package nfsgateway

import (
	"context"
	"github.com/Miraitowa-kawayi/netdisk/internal/repository"
	"github.com/Miraitowa-kawayi/netdisk/internal/service"
	"github.com/Miraitowa-kawayi/netdisk/internal/storage"
	"github.com/Miraitowa-kawayi/netdisk/internal/testutil"
	"github.com/google/uuid"
	client "github.com/willscott/go-nfs-client/nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestNFSProtocolRoundTrip(t *testing.T) {
	dsn := testutil.Database(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := repository.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, err := store.CreateUser(ctx, "nfs-"+uuid.NewString(), "not-password", "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DeleteAccount(context.Background(), owner.ID, "not-password")
	local, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := service.NewFiles(store, local, "local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	fs := NewFS(ctx, files, owner.ID, t.TempDir())
	listener, err := Listen("127.0.0.1:0", "127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go Serve(ctx, listener, fs)
	conn, err := rpc.DialTCP("tcp", listener.Addr().String(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	mount := &client.Mount{Client: conn}
	target, err := mount.Mount("/", rpc.AuthNull)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = target.Mkdir("folder", 0700); err != nil {
		t.Fatal(err)
	}
	file, err := target.OpenFile("folder/test.txt", 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("hello world")); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	node, err := files.LookupPath(ctx, owner.ID, "folder/test.txt")
	if err != nil {
		t.Fatal(err)
	}
	dl, err := files.OpenDownload(ctx, owner.ID, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(dl.Content)
	dl.Content.Close()
	if string(got) != "hello world" {
		t.Fatalf("API reads NFS write: %q", got)
	}
	read, err := target.Open("folder/test.txt")
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if n, readErr := read.ReadAt(buf, 6); n != 5 || (readErr != nil && readErr != io.EOF) {
		t.Fatalf("read n=%d err=%v", n, readErr)
	}
	read.Close()
	if string(buf) != "world" {
		t.Fatalf("NFS random read %q", buf)
	}
	if err = target.Rename("folder/test.txt", "folder/renamed.txt"); err != nil {
		t.Fatal(err)
	}
	entries, err := target.ReadDirPlus("folder")
	if err != nil || len(entries) == 0 {
		t.Fatalf("readdir %v %v", entries, err)
	}
	if err = target.Remove("folder/renamed.txt"); err != nil {
		t.Fatal(err)
	}
	if err = target.RmDir("folder"); err != nil {
		t.Fatal(err)
	}
	t.Log("real TCP NFSv3 mount/create/write/read/rename/readdir/remove passed", time.Now().UTC())
}
