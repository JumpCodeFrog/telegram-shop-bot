// Package web embeds the Mini App static assets served under /app/.
package web

import "embed"

// AppFS holds the Mini App files (index.html, app.js, style.css) under app/.
//
//go:embed app
var AppFS embed.FS
