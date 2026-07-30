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
type Config struct {
	// Server listen address and port.
	ListenAddr string
	// Uploaded images path.
	UploadDir string
	// Maximum allowed image size.
	MaxSize int64
	// How many SHA256 characters are to be used for image names.
	NShaChars int
	// Image TTL: https://en.wikipedia.org/wiki/Time_to_live
	DefaultTTL time.Duration
	MinTTL     time.Duration
	MaxTTL     time.Duration
	// Interval between garbage collector runs.
	DelayGC time.Duration
	// Garbage collector removes images past their TTL.
	RunGarbageCollector bool
	// Project version.
	GitVersion string
}

// Verify config options are valid.
func validateConfig(c Config) error {
	if c.NShaChars < 16 || c.NShaChars > 64 {
		return errors.New("use between 16 and 64 sha256 characters")
	}
	if c.MinTTL < 5*time.Second {
		return errors.New("minimal TTL cannot be less than 5 seconds")
	}
	if c.MinTTL >= c.MaxTTL {
		return errors.New("maximal TTL must be greater than minimal")
	}
	if c.DefaultTTL > c.MaxTTL || c.DefaultTTL < c.MinTTL {
		return errors.New("default TTL must be within the minimal and maximal TTLs")
	}
	return nil
}
