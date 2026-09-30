// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"argus/internal/config"
	"argus/internal/secret"
	"argus/internal/server"
	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// The core host's backup script (deploy/core/host/argus-backup) runs these inside the Argus
// container with `docker exec` - which takes root on the host, so nothing here is reachable from the
// network:
//
//	argus backup-plan          print the backup plan, secrets included, as JSON
//	argus backup-db <path>     write a consistent copy of the Argus database to <path> (under /data)
func runBackupCommand(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: argus backup-plan | argus backup-db <path>")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cfg := config.Load()
	st, err := openStoreForCLI(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "argus: "+err.Error())
		return 1
	}
	defer st.Close()
	switch args[0] {
	case "backup-plan":
		mgr, err := settings.New(ctx, st, zabbix.New("", ""))
		if err != nil {
			fmt.Fprintln(os.Stderr, "argus: settings: "+err.Error())
			return 1
		}
		if err := json.NewEncoder(os.Stdout).Encode(server.LoadBackupPlan(ctx, st, mgr.Location().String())); err != nil {
			fmt.Fprintln(os.Stderr, "argus: "+err.Error())
			return 1
		}
		return 0
	case "backup-db":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: argus backup-db <path>")
			return 2
		}
		if err := server.BackupDBInto(ctx, st, args[1]); err != nil {
			fmt.Fprintln(os.Stderr, "argus: backup-db: "+err.Error())
			return 1
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "argus: unknown command "+args[0])
	return 2
}

// openStoreForCLI opens the database with its at-rest key and checks the key matches, without any of
// the server's one-off repairs (re-encryption, key reset): a helper command never changes secrets.
func openStoreForCLI(ctx context.Context, cfg config.Config) (*store.Store, error) {
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	cipher, _, err := secret.Load(cfg.SecretKey, cfg.DataDir)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("at-rest key: %w", err)
	}
	st.SetCipher(cipher)
	if err := st.VerifyCipher(ctx); err != nil {
		st.Close()
		return nil, fmt.Errorf("the at-rest key does not match the database: %w", err)
	}
	return st, nil
}
