// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"io"

	"github.com/orevcs/ore/internal/ledger"
)

// runMigrate implements `ore migrate [status|apply]`.
func runMigrate(args []string, w io.Writer) error {
	fl := parseFlags(args)
	cfg, err := loadConfig(fl)
	if err != nil {
		return err
	}
	sub := "status"
	if len(fl.pos) > 0 {
		sub = fl.pos[0]
	}
	ctx := context.Background()
	switch sub {
	case "status":
		db, err := ledger.OpenWith(ctx, cfg.dbTarget(), ledger.Options{NoMigrate: true})
		if err != nil {
			return err
		}
		defer db.Close()
		return printMigrations(ctx, db, w)
	case "apply":
		db, err := ledger.Open(ctx, cfg.dbTarget())
		if err != nil {
			return err
		}
		defer db.Close()
		fmt.Fprintf(w, "migrations applied on %s (%s)\n", db.Target(), db.Dialect())
		return printMigrations(ctx, db, w)
	}
	return fmt.Errorf("usage: ore migrate [status|apply] [--db URL | --data DIR]")
}

func printMigrations(ctx context.Context, db *ledger.DB, w io.Writer) error {
	ms, err := db.Migrations(ctx)
	if err != nil {
		return err
	}
	pending := 0
	for _, m := range ms {
		state := m.AppliedAt
		if state == "" {
			state = "pending"
			pending++
		}
		fmt.Fprintf(w, "%04d  %-28s %s\n", m.Version, m.Name, state)
	}
	fmt.Fprintf(w, "%d migrations, %d pending, database %s (%s)\n", len(ms), pending, db.Target(), db.Dialect())
	return nil
}
