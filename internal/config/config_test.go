package config

import "testing"

func TestDeploymentOptions(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("NETDISK_DATABASE_URL", "test")
	t.Setenv("NETDISK_JWT_SECRET", "test")
	allKeys := []string{
		"NETDISK_ALLOW_REGISTER", "ALLOW_REGISTER",
		"NETDISK_ALLOW_GLOBAL_INSTANT_UPLOAD", "ALLOW_GLOBAL_INSTANT_UPLOAD",
		"NETDISK_COOKIE_SECURE", "COOKIE_SECURE",
		"NETDISK_TRUSTED_PROXY_CIDRS", "TRUSTED_PROXY_CIDRS",
	}
	for _, key := range allKeys {
		t.Setenv(key, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowRegister || cfg.AllowGlobalInstantUpload || cfg.CookieSecure != "auto" || len(cfg.TrustedProxyCIDRs) != 0 {
		t.Fatal("unexpected deployment defaults")
	}

	// 旧无前缀环境变量仍可兼容生效。
	t.Setenv("ALLOW_REGISTER", "false")
	t.Setenv("ALLOW_GLOBAL_INSTANT_UPLOAD", "true")
	t.Setenv("COOKIE_SECURE", "true")
	t.Setenv("TRUSTED_PROXY_CIDRS", "127.0.0.1/32, ::1/128")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AllowRegister || !cfg.AllowGlobalInstantUpload || cfg.CookieSecure != "true" || len(cfg.TrustedProxyCIDRs) != 2 {
		t.Fatal("legacy deployment overrides ignored")
	}

	// 统一后的 NETDISK_* 前缀优先于旧名称。
	t.Setenv("NETDISK_ALLOW_REGISTER", "true")
	t.Setenv("NETDISK_ALLOW_GLOBAL_INSTANT_UPLOAD", "false")
	t.Setenv("NETDISK_COOKIE_SECURE", "false")
	t.Setenv("NETDISK_TRUSTED_PROXY_CIDRS", "10.0.0.0/8")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowRegister || cfg.AllowGlobalInstantUpload || cfg.CookieSecure != "false" || len(cfg.TrustedProxyCIDRs) != 1 {
		t.Fatal("prefixed deployment overrides did not take precedence")
	}

	for _, key := range []string{"NETDISK_ALLOW_REGISTER", "NETDISK_ALLOW_GLOBAL_INSTANT_UPLOAD", "NETDISK_COOKIE_SECURE", "NETDISK_TRUSTED_PROXY_CIDRS"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "invalid")
			if _, err := Load(); err == nil {
				t.Fatal("invalid option accepted")
			}
		})
	}
}
