// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Ivan Kovmir
package main

import (
	"errors"
	"time"
)

// Overridable via -ldflags "-X main.gitVersion=<tag>".
var gitVersion = "dev"

// Server config.
type config struct {
	// Server listen address and port.
	listenAddr string
	// Uploaded images path.
	uploadDir string
	// Maximum allowed image size.
	maxSize int64
	// How many SHA256 characters are to be used for image names.
	nShaChars int
	// Image TTL: https://en.wikipedia.org/wiki/Time_to_live
	defaultTTL time.Duration
	minTTL     time.Duration
	maxTTL     time.Duration
	// Interval between garbage collector runs.
	delayGC time.Duration
	// Garbage collector removes images past their TTL.
	runGarbageCollector bool
	// Project version.
	gitVersion string
}

// Verify config options are valid.
func validateConfig(c config) error {
	if c.nShaChars < 16 || c.nShaChars > 64 {
		return errors.New("use between 16 and 64 sha256 characters")
	}
	if c.minTTL < 5*time.Second {
		return errors.New("minimal TTL cannot be less than 5 seconds")
	}
	if c.minTTL >= c.maxTTL {
		return errors.New("maximal TTL must be greater than minimal")
	}
	if c.defaultTTL > c.maxTTL || c.defaultTTL < c.minTTL {
		return errors.New("default TTL must be within the minimal and maximal TTLs")
	}
	return nil
}
