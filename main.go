// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Ivan Kovmir

// Main project code resides in server.go.
// This file creates HTTP server instance and runs the listener.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	cfg := Config{
		RunGarbageCollector: true,
		MaxSize:             32 << 20,
		NShaChars:           16,
		DefaultTTL:          72 * time.Hour,
		MinTTL:              time.Hour,
		MaxTTL:              168 * time.Hour,
		ListenAddr:          ":8077",
		UploadDir:           "./uploads",
		DelayGC:             10 * time.Second,
	}

	flag.BoolVar(&cfg.RunGarbageCollector, "del", cfg.RunGarbageCollector, "delete images past the expiration time?")
	flag.Int64Var(&cfg.MaxSize, "maxsize", cfg.MaxSize, "maximal uploaded image size in bytes")
	flag.IntVar(&cfg.NShaChars, "sumlen", cfg.NShaChars, "number of sha256 characters used for image file names")
	flag.DurationVar(&cfg.DefaultTTL, "ttl", cfg.DefaultTTL, "default image TTL")
	flag.DurationVar(&cfg.MinTTL, "minttl", cfg.MinTTL, "minimal image TTL")
	flag.DurationVar(&cfg.MaxTTL, "maxttl", cfg.MaxTTL, "maximal image TTL")
	flag.StringVar(&cfg.ListenAddr, "addr", cfg.ListenAddr, "server listen address")
	flag.StringVar(&cfg.UploadDir, "dir", cfg.UploadDir, "uploaded images directory")
	flag.DurationVar(&cfg.DelayGC, "delay", cfg.DelayGC, "expired image checker interval")

	flag.Parse()
	cfg.GitVersion = gitVersion

	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}

// Start server.
func run(cfg Config) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	s, err := NewServer(cfg, uploadFormHTML, faviconData)
	if err != nil {
		return err
	}
	defer s.Close()

	if cfg.RunGarbageCollector {
		s.CleanOrphanLinks()
		go s.gcLoop(cfg.DelayGC)
	}

	srv := http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Println("listening on", cfg.ListenAddr)
	return srv.ListenAndServe()
}
