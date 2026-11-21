package config

import "testing"

func TestDeploymentOptions(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("NETDISK_DATABASE_URL", "test")
	t.Setenv("NETDISK_JWT_SECRET", "test")
	for _, key := range []string{"ALLOW_REGISTER", "ALLOW_GLOBAL_INSTANT_UPLOAD", "COOKIE_SECURE", "TRUSTED_PROXY_CIDRS"} {
		t.Setenv(key, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowRegister || cfg.AllowGlobalInstantUpload || cfg.CookieSecure != "auto" || len(cfg.TrustedProxyCIDRs) != 0 {
		t.Fatal("unexpected deployment defaults")
	}
	t.Setenv("ALLOW_REGISTER", "false")
	t.Setenv("ALLOW_GLOBAL_INSTANT_UPLOAD", "true")
	t.Setenv("COOKIE_SECURE", "true")
	t.Setenv("TRUSTED_PROXY_CIDRS", "127.0.0.1/32, ::1/128")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AllowRegister || !cfg.AllowGlobalInstantUpload || cfg.CookieSecure != "true" || len(cfg.TrustedProxyCIDRs) != 2 {
		t.Fatal("deployment overrides ignored")
	}
	for _, key := range []string{"ALLOW_REGISTER", "ALLOW_GLOBAL_INSTANT_UPLOAD", "COOKIE_SECURE", "TRUSTED_PROXY_CIDRS"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "invalid")
			if _, err := Load(); err == nil {
				t.Fatal("invalid option accepted")
			}
		})
	}
}
