// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Ivan Kovmir
package main

import (
	"testing"
	"time"
)

func TestValidateConfig(t *testing.T) {
	// valid baseline config used across subtests
	valid := config{
		nShaChars:  32,
		minTTL:     5 * time.Second,
		maxTTL:     60 * time.Second,
		defaultTTL: 30 * time.Second,
	}

	tests := []struct {
		name    string
		mutate  func(c *config)
		wantErr string
	}{
		// --- nShaChars ---
		{
			name:    "nShaChars too low",
			mutate:  func(c *config) { c.nShaChars = 15 },
			wantErr: "use between 16 and 64 sha256 characters",
		},
		{
			name:    "nShaChars boundary low (16 is valid)",
			mutate:  func(c *config) { c.nShaChars = 16 },
			wantErr: "",
		},
		{
			name:    "nShaChars too high",
			mutate:  func(c *config) { c.nShaChars = 65 },
			wantErr: "use between 16 and 64 sha256 characters",
		},
		{
			name:    "nShaChars boundary high (64 is valid)",
			mutate:  func(c *config) { c.nShaChars = 64 },
			wantErr: "",
		},

		// --- minTTL ---
		{
			name:    "minTTL below 5 seconds",
			mutate:  func(c *config) { c.minTTL = 4 * time.Second },
			wantErr: "minimal TTL cannot be less than 5 seconds",
		},
		{
			name:    "minTTL boundary (5s is valid)",
			mutate:  func(c *config) { c.minTTL = 5 * time.Second },
			wantErr: "",
		},

		// --- minTTL >= maxTTL ---
		{
			name:    "minTTL equals maxTTL",
			mutate:  func(c *config) { c.minTTL = 60 * time.Second; c.maxTTL = 60 * time.Second },
			wantErr: "maximal TTL must be greater than minimal",
		},
		{
			name:    "minTTL greater than maxTTL",
			mutate:  func(c *config) { c.minTTL = 61 * time.Second; c.maxTTL = 60 * time.Second },
			wantErr: "maximal TTL must be greater than minimal",
		},

		// --- defaultTTL out of range ---
		{
			name:    "defaultTTL below minTTL",
			mutate:  func(c *config) { c.minTTL = 10 * time.Second; c.defaultTTL = 9 * time.Second },
			wantErr: "default TTL must be within the minimal and maximal TTLs",
		},
		{
			name:    "defaultTTL above maxTTL",
			mutate:  func(c *config) { c.defaultTTL = 61 * time.Second },
			wantErr: "default TTL must be within the minimal and maximal TTLs",
		},

		// --- valid cases ---
		{
			name:    "fully valid config",
			mutate:  func(c *config) {},
			wantErr: "",
		},
		{
			name:    "defaultTTL equals minTTL is valid",
			mutate:  func(c *config) { c.minTTL = 10 * time.Second; c.defaultTTL = 10 * time.Second },
			wantErr: "",
		},
		{
			name:    "defaultTTL equals maxTTL is valid",
			mutate:  func(c *config) { c.defaultTTL = 60 * time.Second },
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := valid // fresh copy each iteration
			tt.mutate(&c)

			err := validateConfig(c)

			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("expected no error, got %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected error %q, got nil", tt.wantErr)
			case tt.wantErr != "" && err.Error() != tt.wantErr:
				t.Fatalf("expected error %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}
