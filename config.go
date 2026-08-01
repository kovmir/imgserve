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
	uploadPath string
	// Maximum allowed image size.
	maxImgSize int64
	// How many SHA256 characters are to be used for image names.
	hashLen int
	// Image TTL: https://en.wikipedia.org/wiki/Time_to_live
	defaultTTL time.Duration
	minTTL     time.Duration
	maxTTL     time.Duration
	// Remove images past their TTL?
	cleanUpImages bool
	// Interval between expired image checks.
	imageCleanUpDelay time.Duration
	// Project version.
	gitVersion string
}

// Verify config options are valid.
func validateConfig(c config) error {
	if c.hashLen < 16 || c.hashLen > 64 {
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
