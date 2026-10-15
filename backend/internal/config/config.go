package config

import (
	"encoding/hex"
	"errors"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/maildelivery"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	TrustedProxies   []netip.Prefix
	CandidateIPLimit int
	Address          string
	DatabaseURL      string
	SecureCookie     bool
	Origins          []string
	AuthKey          []byte
	PublicURL        string
	MailMode         string
	MailSender       maildelivery.Sender
}

func DatabaseURL() (string, error) {
	value := os.Getenv("DATABASE_URL")
	if value == "" {
		return "", errors.New("DATABASE_URL is required")
	}
	return value, nil
}

func Load() (Config, error) {
	c := Config{Address: os.Getenv("HTTP_ADDR"), DatabaseURL: os.Getenv("DATABASE_URL")}
	if c.Address == "" {
		c.Address = "127.0.0.1:8080"
	}
	if c.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	c.SecureCookie = true
	if v := os.Getenv("COOKIE_SECURE"); v != "" {
		secure, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, errors.New("invalid COOKIE_SECURE")
		}
		c.SecureCookie = secure
	}
	origins := os.Getenv("APP_ORIGINS")
	if origins == "" {
		origins = "http://127.0.0.1:8088,http://localhost:8088,http://127.0.0.1:5173,http://localhost:5173"
	}
	for _, origin := range strings.Split(origins, ",") {
		origin = strings.TrimSpace(origin)
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return Config{}, errors.New("invalid APP_ORIGINS")
		}
		if c.SecureCookie && u.Scheme != "https" {
			return Config{}, errors.New("secure cookie mode requires HTTPS APP_ORIGINS; local development must explicitly set COOKIE_SECURE=false")
		}
		c.Origins = append(c.Origins, origin)
	}

	if value := os.Getenv("TRUSTED_PROXY_CIDRS"); value != "" {
		parts := strings.Split(value, ",")
		if len(parts) > 32 {
			return Config{}, errors.New("too many TRUSTED_PROXY_CIDRS")
		}
		for _, part := range parts {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(part))
			if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
				return Config{}, errors.New("invalid TRUSTED_PROXY_CIDRS; use explicit proxy networks, never /0")
			}
			c.TrustedProxies = append(c.TrustedProxies, prefix.Masked())
		}
	}
	c.CandidateIPLimit = 600
	if value := os.Getenv("CANDIDATE_IP_LIMIT_PER_MINUTE"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 100000 {
			return Config{}, errors.New("invalid CANDIDATE_IP_LIMIT_PER_MINUTE")
		}
		c.CandidateIPLimit = n
	}
	var err error
	c.AuthKey, err = hex.DecodeString(os.Getenv("AUTH_ENCRYPTION_KEY"))
	if err != nil || len(c.AuthKey) != 32 {
		return Config{}, errors.New("AUTH_ENCRYPTION_KEY must be a preserved 32-byte hex key")
	}
	c.PublicURL = os.Getenv("APP_PUBLIC_URL")
	if c.PublicURL == "" {
		c.PublicURL = c.Origins[0]
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return Config{}, errors.New("invalid APP_PUBLIC_URL")
	}
	loopback := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if (c.SecureCookie || !loopback) && u.Scheme != "https" {
		return Config{}, errors.New("APP_PUBLIC_URL requires HTTPS outside local development")
	}
	c.MailMode = os.Getenv("AUTH_MAIL_MODE")
	if c.MailMode == "" {
		c.MailMode = "disabled"
	}
	if c.MailMode != "disabled" && c.MailMode != "file" && c.MailMode != "smtp" {
		return Config{}, errors.New("invalid AUTH_MAIL_MODE")
	}
	if c.MailMode == "file" && (c.SecureCookie || !loopback) {
		return Config{}, errors.New("file mail is for local HTTP development only")
	}
	directory := os.Getenv("AUTH_MAIL_DIR")
	if directory == "" {
		directory = ".mailbox"
	}
	directory, _ = filepath.Abs(directory)
	c.MailSender = maildelivery.Sender{Mode: c.MailMode, Directory: directory, Address: os.Getenv("SMTP_ADDR"), Username: os.Getenv("SMTP_USERNAME"), Password: os.Getenv("SMTP_PASSWORD"), From: os.Getenv("SMTP_FROM")}
	if c.MailMode == "smtp" {
		_, port, e := net.SplitHostPort(c.MailSender.Address)
		n, pe := strconv.Atoi(port)
		_, fe := mail.ParseAddress(c.MailSender.From)
		if e != nil || pe != nil || n < 1 || n > 65535 || fe != nil {
			return Config{}, errors.New("SMTP_ADDR and SMTP_FROM are required")
		}
		if (c.MailSender.Username == "") != (c.MailSender.Password == "") {
			return Config{}, errors.New("incomplete SMTP credentials")
		}
	}
	return c, nil
}
