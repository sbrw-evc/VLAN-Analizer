// Package web embeds the built React app (web/dist, produced by npm run build).
package web

import "embed"

//go:embed all:dist
var Assets embed.FS
