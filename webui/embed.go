// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

// Package webui embeds the compiled React admin frontend.
// Build the frontend before compiling the binary:
//
//	cd webui && npm install && npm run build
package webui

import "embed"

// Dist contains the compiled React application from webui/dist/.
// The `all:` prefix ensures hidden files (e.g. .vite) are also included.
//
//go:embed all:dist
var Dist embed.FS
