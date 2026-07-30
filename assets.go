// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Ivan Kovmir
package main

import _ "embed"

//go:embed upload_form.html
var uploadFormHTML string

//go:embed favicon.ico
var faviconData []byte
