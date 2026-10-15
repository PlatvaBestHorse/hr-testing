package config

import (
	"strings"
	"testing"
)

func TestCookieAndOriginConfig(t *testing.T) {
	t.Setenv("AUTH_ENCRYPTION_KEY", strings.Repeat("ab", 32))
	t.Setenv("APP_PUBLIC_URL", "")
	t.Setenv("AUTH_MAIL_MODE", "disabled")
	t.Setenv("DATABASE_URL", "postgres://fixture")
	t.Setenv("COOKIE_SECURE", "true")
	t.Setenv("APP_ORIGINS", "https://hr.example")
	c, err := Load()
	if err != nil || !c.SecureCookie {
		t.Fatal("HTTPS mode must use secure cookie:", err)
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

func TestProxyAndCandidateLimits(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://fixture")
	t.Setenv("AUTH_ENCRYPTION_KEY", strings.Repeat("ab", 32))
	t.Setenv("COOKIE_SECURE", "false")
	t.Setenv("APP_ORIGINS", "http://127.0.0.1:8088")
	t.Setenv("APP_PUBLIC_URL", "")
	t.Setenv("AUTH_MAIL_MODE", "disabled")
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	t.Setenv("CANDIDATE_IP_LIMIT_PER_MINUTE", "")
	c, err := Load()
	if err != nil || len(c.TrustedProxies) != 0 || c.CandidateIPLimit != 600 {
		t.Fatal("unsafe defaults", err)
	}
	t.Setenv("TRUSTED_PROXY_CIDRS", "172.30.254.10/32, ::1/128")
	c, err = Load()
	if err != nil || len(c.TrustedProxies) != 2 {
		t.Fatal("valid proxy list rejected", err)
	}
	for _, v := range []string{"*", "0.0.0.0/0", "::/0", "bad", "127.0.0.1", "::ffff:127.0.0.1/128", "172.30.254.10/32,"} {
		t.Setenv("TRUSTED_PROXY_CIDRS", v)
		if _, err = Load(); err == nil {
			t.Fatalf("unsafe proxy list accepted: %q", v)
		}
	}
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	for _, v := range []string{"0", "-1", "100001", "bad"} {
		t.Setenv("CANDIDATE_IP_LIMIT_PER_MINUTE", v)
		if _, err = Load(); err == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	t.Setenv("CANDIDATE_IP_LIMIT_PER_MINUTE", "1200")
	c, err = Load()
	if err != nil || c.CandidateIPLimit != 1200 {
		t.Fatal("valid limit rejected", err)
	}
}
