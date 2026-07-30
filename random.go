// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Ivan Kovmir
package main

import "math/rand"

// Generate a string with random numbers and ASCII characters.
func defaultRandomID(nChars int) string {
	const charSet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, nChars)
	for i := range b {
		b[i] = charSet[rand.Intn(len(charSet))]
	}
	return string(b)
}
