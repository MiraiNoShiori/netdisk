// Command server 是 NetDisk 的 HTTP 服务入口。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MiraiNoShiori/netdisk/internal/config"
	"github.com/MiraiNoShiori/netdisk/internal/handler"
	"github.com/MiraiNoShiori/netdisk/internal/nfsgateway"
	"github.com/MiraiNoShiori/netdisk/internal/repository"
	"github.com/MiraiNoShiori/netdisk/internal/service"
	"github.com/MiraiNoShiori/netdisk/internal/storage"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("server exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// SIGINT / SIGTERM 取消 context，触发优雅退出。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := repository.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	// 启动时探测数据库，尽早暴露配置错误。
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := store.Ping(pingCtx); err != nil {
		return err
	}

	if err := store.Migrate(ctx); err != nil {
		return err
	}

	// 内容存储后端：上层只依赖 storage.Storage 接口。
	local, err := storage.NewLocal(cfg.StorageDir)
	if err != nil {
		return err
	}
	backends := map[string]storage.Storage{"local": local}
	if cfg.S3.Endpoint != "" {
		s3, err := storage.NewS3(cfg.S3)
		if err != nil {
			return err
		}
		checkCtx, checkCancel := context.WithTimeout(ctx, 15*time.Second)
		err = s3.EnsureBucket(checkCtx)
		checkCancel()
		if err != nil {
			return fmt.Errorf("S3 bucket: %w", err)
		}
		backends["s3"] = s3
	}
	selected, ok := backends[cfg.StorageDriver]
	if !ok {
		return fmt.Errorf("storage backend %q is not configured", cfg.StorageDriver)
	}
	blobStore := &storage.Backends{Storage: selected, Default: cfg.StorageDriver, Stores: backends}

	tokens := service.NewTokens(cfg.JWTSecret, cfg.JWTTTL)
	auth := service.NewAuthWithOptions(store, tokens, cfg.RefreshTTL, cfg.DefaultQuotaBytes)
	files := service.NewFiles(store, blobStore, cfg.StorageDriver, logger)
	uploads := service.NewUploads(store, blobStore, cfg.StorageDriver, logger)
	go uploads.RunCleanup(ctx, service.MaintenanceOptions{
		Files:             files,
		TrashRetention:    cfg.TrashRetention,
		AutoTierThreshold: cfg.AutoTierThreshold,
		AutoTierMinSize:   cfg.AutoTierMinSize,
	})
	shares := service.NewShares(store, files, logger)
	if cfg.NFSAddr != "" {
		user, err := store.GetUserByUsername(ctx, cfg.NFSUser)
		if err != nil {
			return fmt.Errorf("NFS export user: %w", err)
		}
		listener, err := nfsgateway.Listen(cfg.NFSAddr, cfg.NFSClients)
		if err != nil {
			return err
		}
		defer listener.Close()
		fs := nfsgateway.NewFS(ctx, files, user.ID, "")
		go func() {
			if err := nfsgateway.Serve(ctx, listener, fs); err != nil && ctx.Err() == nil {
				logger.Error("NFS stopped", "error", err)
				stop()
			}
		}()
		logger.Info("NFS export listening", "addr", cfg.NFSAddr, "user", cfg.NFSUser)
	}

	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: handler.NewRouter(handler.Deps{
			Cfg:     cfg,
			Logger:  logger,
			Store:   store,
			Auth:    auth,
			Files:   files,
			Uploads: uploads,
			Shares:  shares,
		}),
		// 只设 ReadHeaderTimeout；上传下载是大体积长连接，Read/WriteTimeout 会中途掐断。
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("netdisk listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	return srv.Shutdown(shutdownCtx)
}
