// Package config 聚合运行期配置：优先环境变量，其次读 .env 文件。
package config

import (
	"bufio"
	"errors"
	"fmt"
	"github.com/MiraiNoShiori/netdisk/internal/storage"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 是服务启动所需的全部配置。
type Config struct {
	AllowGlobalInstantUpload     bool
	Addr                         string
	DatabaseURL                  string
	JWTSecret                    string
	JWTTTL                       time.Duration
	RefreshTTL                   time.Duration
	DefaultQuotaBytes            int64
	TrashRetention               time.Duration
	CORSOrigins                  []string
	AutoTierThreshold            int64
	AutoTierMinSize              int64
	StorageDriver                string
	StorageDir                   string
	S3                           storage.S3Config
	NFSAddr, NFSUser, NFSClients string
}

const dotEnvFile = ".env"

// Load 先加载 .env（不存在则跳过）再读环境变量；已存在的环境变量优先，不被 .env 覆盖。
func Load() (*Config, error) {
	if err := loadDotEnv(dotEnvFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load %s: %w", dotEnvFile, err)
	}

	ttl, err := time.ParseDuration(envOr("NETDISK_JWT_TTL", "24h"))
	if err != nil {
		return nil, fmt.Errorf("parse NETDISK_JWT_TTL: %w", err)
	}
	refreshTTL, err := time.ParseDuration(envOr("NETDISK_REFRESH_TTL", "168h"))
	if err != nil {
		return nil, fmt.Errorf("parse NETDISK_REFRESH_TTL: %w", err)
	}
	trashRetention, err := time.ParseDuration(envOr("NETDISK_TRASH_RETENTION", "720h"))
	if err != nil {
		return nil, fmt.Errorf("parse NETDISK_TRASH_RETENTION: %w", err)
	}
	defaultQuota, err := strconv.ParseInt(envOr("NETDISK_DEFAULT_QUOTA_BYTES", "10737418240"), 10, 64)
	if err != nil || defaultQuota < 0 {
		return nil, fmt.Errorf("parse NETDISK_DEFAULT_QUOTA_BYTES: must be non-negative integer")
	}
	autoTierThreshold, err := strconv.ParseInt(envOr("NETDISK_AUTO_TIER_THRESHOLD", "0"), 10, 64)
	if err != nil || autoTierThreshold < 0 {
		return nil, fmt.Errorf("parse NETDISK_AUTO_TIER_THRESHOLD: must be non-negative integer")
	}
	autoTierMinSize, err := strconv.ParseInt(envOr("NETDISK_AUTO_TIER_MIN_SIZE", "1048576"), 10, 64)
	if err != nil || autoTierMinSize < 0 {
		return nil, fmt.Errorf("parse NETDISK_AUTO_TIER_MIN_SIZE: must be non-negative integer")
	}
	var corsOrigins []string
	if rawCORS := strings.TrimSpace(envOr("NETDISK_CORS_ORIGINS", "")); rawCORS != "" {
		for _, origin := range strings.Split(rawCORS, ",") {
			if o := strings.TrimSpace(origin); o != "" {
				corsOrigins = append(corsOrigins, o)
			}
		}
	}

	cfg := &Config{
		Addr:              envOr("NETDISK_ADDR", ":8081"),
		DatabaseURL:       envOr("NETDISK_DATABASE_URL", ""),
		JWTSecret:         envOr("NETDISK_JWT_SECRET", ""),
		JWTTTL:            ttl,
		RefreshTTL:        refreshTTL,
		DefaultQuotaBytes: defaultQuota,
		TrashRetention:    trashRetention,
		CORSOrigins:       corsOrigins,
		AutoTierThreshold: autoTierThreshold,
		AutoTierMinSize:   autoTierMinSize,
		StorageDriver:     envOr("NETDISK_STORAGE_DRIVER", "local"),
		StorageDir:        envOr("NETDISK_STORAGE_DIR", "./data/blobs"),
	}

	cfg.AllowGlobalInstantUpload, err = strconv.ParseBool(envOr("ALLOW_GLOBAL_INSTANT_UPLOAD", "false"))
	if err != nil {
		return nil, fmt.Errorf("ALLOW_GLOBAL_INSTANT_UPLOAD: %w", err)
	}

	secure, err := strconv.ParseBool(envOr("NETDISK_S3_USE_SSL", "false"))
	if err != nil {
		return nil, fmt.Errorf("NETDISK_S3_USE_SSL: %w", err)
	}
	cfg.S3 = storage.S3Config{Endpoint: envOr("NETDISK_S3_ENDPOINT", ""), PublicEndpoint: envOr("NETDISK_S3_PUBLIC_ENDPOINT", ""), AccessKey: envOr("NETDISK_S3_ACCESS_KEY", ""), SecretKey: envOr("NETDISK_S3_SECRET_KEY", ""), Bucket: envOr("NETDISK_S3_BUCKET", "netdisk"), Region: envOr("NETDISK_S3_REGION", "us-east-1"), Secure: secure}
	cfg.NFSAddr = envOr("NETDISK_NFS_ADDR", "")
	cfg.NFSUser = envOr("NETDISK_NFS_USER", "")
	cfg.NFSClients = envOr("NETDISK_NFS_CLIENTS", "127.0.0.1/32,::1/128")
	if ttl <= 0 {
		return nil, fmt.Errorf("NETDISK_JWT_TTL must be positive")
	}
	if refreshTTL <= 0 {
		return nil, fmt.Errorf("NETDISK_REFRESH_TTL must be positive")
	}
	if trashRetention <= 0 {
		return nil, fmt.Errorf("NETDISK_TRASH_RETENTION must be positive")
	}
	var missing []string
	if cfg.DatabaseURL == "" {
		missing = append(missing, "NETDISK_DATABASE_URL")
	}
	if cfg.JWTSecret == "" {
		missing = append(missing, "NETDISK_JWT_SECRET")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("缺少必填配置 %s（先 cp .env.example .env）", strings.Join(missing, ", "))
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// loadDotEnv 是最小的 KEY=VALUE 解析器：忽略空行与 # 注释，去掉值两端引号，
// 且不覆盖已存在的环境变量。
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return sc.Err()
}
