package main

import (
	"context"
	"encoding/json"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/config"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/database"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/identity"
	"io"
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
	var input identity.BootstrapInput
	d := json.NewDecoder(io.LimitReader(os.Stdin, 16*1024))
	d.DisallowUnknownFields()
	if err = d.Decode(&input); err != nil {
		slog.Error("invalid bootstrap input")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := database.Open(ctx, url)
	if err != nil {
		slog.Error("database configuration invalid")
		os.Exit(1)
	}
	defer p.Close()
	if err = identity.Bootstrap(ctx, p, input); err != nil {
		slog.Error("bootstrap failed; check account details, database readiness or existing organization")
		os.Exit(1)
	}
	slog.Info("first organization and administrator created")
}
