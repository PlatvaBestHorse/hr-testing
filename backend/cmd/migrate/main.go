package main

import (
	"context"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/config"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/database"
	"log/slog"
	"os"
	"time"
)

func main() {
	url, err := config.DatabaseURL()
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	p, err := database.Open(ctx, url)
	if err != nil {
		slog.Error("invalid database configuration")
		os.Exit(1)
	}
	defer p.Close()
	if err = database.Migrate(ctx, p); err != nil {
		slog.Error(database.RedactError(err))
		os.Exit(1)
	}
	slog.Info("database migrations applied")
}
