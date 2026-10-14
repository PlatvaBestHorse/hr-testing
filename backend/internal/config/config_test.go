package config

import "testing"

func TestCookieAndOriginConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://fixture")
	t.Setenv("COOKIE_SECURE", "true")
	t.Setenv("APP_ORIGINS", "https://hr.example")
	c, err := Load()
	if err != nil || !c.SecureCookie {
		t.Fatal("HTTPS mode must use secure cookie")
	}
	t.Setenv("APP_ORIGINS", "http://hr.example")
	if _, err = Load(); err == nil {
		t.Fatal("secure mode accepted insecure origin")
	}
	t.Setenv("COOKIE_SECURE", "false")
	t.Setenv("APP_ORIGINS", "http://127.0.0.1:8088")
	if _, err = Load(); err != nil {
		t.Fatal("explicit local mode rejected")
	}
	for _, origin := range []string{"null", "*", "https://hr.example/path", "https://user:password@hr.example"} {
		t.Setenv("APP_ORIGINS", origin)
		if _, err = Load(); err == nil {
			t.Fatal("invalid origin accepted")
		}
	}
}
