package config

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Address      string
	DatabaseURL  string
	SecureCookie bool
	Origins      []string
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
	return c, nil
}
