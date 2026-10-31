/*
 * Copyright (C) 2026 Kingkor Roy Tirtho and Spotube Contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

// Command spotube-jamd is a self-hosted Spotube jam room server: the same protocol as
// the Cloudflare Worker in ../src, with rooms kept in memory and SQLite instead of
// Durable Objects. It runs as a single process; rooms are not shared between instances.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func main() {
	addr := env("JAM_ADDR", ":8787")
	dbPath := env("JAM_DB", "jam.db")
	// Behind a reverse proxy the peer address is the proxy's: name the header that
	// carries the client's (X-Forwarded-For, X-Real-IP, CF-Connecting-IP). Only set it
	// when the proxy overwrites that header, or clients can spoof their IP.
	ipHeader := os.Getenv("JAM_IP_HEADER")

	s, err := openStore(dbPath)
	if err != nil {
		log.Fatalf("open %s: %v", dbPath, err)
	}
	defer s.Close()

	h := newHub(s)
	srv := &http.Server{
		Addr:              addr,
		Handler:           newServer(h, ipHeader),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		for range time.Tick(30 * time.Second) {
			h.sweep()
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	log.Printf("jam server listening on %s (db %s)", addr, dbPath)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
