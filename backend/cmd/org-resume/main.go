// Maintenance-only operation; receives the organization ID through stdin.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/config"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/database"
	"io"
	"os"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Organization resume failed; check ID, owner credentials and migrations.")
		os.Exit(1)
	}
	fmt.Println("Organization resumed. Users must sign in again.")
}
func run() error {
	var in struct {
		ID string `json:"organization_id"`
	}
	d := json.NewDecoder(io.LimitReader(os.Stdin, 2048))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	if len(in.ID) != 36 {
		return fmt.Errorf("invalid ID")
	}
	if _, err := hex.DecodeString(strings.ReplaceAll(in.ID, "-", "")); err != nil {
		return err
	}
	url, err := config.DatabaseURL()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	var role string
	if err = pool.QueryRow(ctx, "SELECT current_user").Scan(&role); err != nil || role == "hr_app" {
		return fmt.Errorf("maintenance owner required")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var id string
	if err = tx.QueryRow(ctx, "SELECT id FROM organizations WHERE id=$1 AND status IN ('suspended','archived') FOR UPDATE", in.ID).Scan(&id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE organizations SET status='active',settings_revision=settings_revision+1 WHERE id=$1", id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM auth_sessions WHERE organization_id=$1", id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(organization_id,action,object_type,object_id) VALUES($1,'organization.resume_cli','organization',$1)", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
