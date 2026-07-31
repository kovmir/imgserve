// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Ivan Kovmir

// Main project code resides in server.go.
// This file creates HTTP server instance and runs the listener.
package main

import (
	"flag"
	"net/http"
	"time"
)

func main() {
	cfg := config{
		runGarbageCollector: true,
		maxSize:             32 << 20,
		nShaChars:           16,
		defaultTTL:          72 * time.Hour,
		minTTL:              time.Hour,
		maxTTL:              168 * time.Hour,
		listenAddr:          ":8077",
		uploadDir:           "./uploads",
		delayGC:             10 * time.Second,
	}

	flag.BoolVar(&cfg.runGarbageCollector, "del", cfg.runGarbageCollector, "delete images past the expiration time?")
	flag.Int64Var(&cfg.maxSize, "maxsize", cfg.maxSize, "maximal uploaded image size in bytes")
	flag.IntVar(&cfg.nShaChars, "sumlen", cfg.nShaChars, "number of sha256 characters used for image file names")
	flag.DurationVar(&cfg.defaultTTL, "ttl", cfg.defaultTTL, "default image TTL")
	flag.DurationVar(&cfg.minTTL, "minttl", cfg.minTTL, "minimal image TTL")
	flag.DurationVar(&cfg.maxTTL, "maxttl", cfg.maxTTL, "maximal image TTL")
	flag.StringVar(&cfg.listenAddr, "addr", cfg.listenAddr, "server listen address")
	flag.StringVar(&cfg.uploadDir, "dir", cfg.uploadDir, "uploaded images directory")
	flag.DurationVar(&cfg.delayGC, "delay", cfg.delayGC, "expired image checker interval")

	flag.Parse()
	cfg.gitVersion = gitVersion

	if err := run(cfg); err != nil {
		panic(err)
	}
}

// Start server.
func run(cfg config) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	s, err := newServer(cfg, uploadFormHTML, faviconData)
	if err != nil {
		return err
	}
	defer s.close()

	if cfg.runGarbageCollector {
		s.cleanOrphanLinks()
		go s.gcLoop(cfg.delayGC)
	}

	srv := http.Server{
		Addr:              cfg.listenAddr,
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	s.logger.Info("listening", "interface", cfg.listenAddr)
	return srv.ListenAndServe()
}
