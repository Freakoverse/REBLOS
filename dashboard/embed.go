// Package dashboard embeds the built admin dashboard (a Vite/React app) so the
// node can serve it at /dashboard with no external files. The dist/ directory
// is committed to the repo — a fork can `go build` and run the dashboard
// without a Node toolchain; only editing the dashboard source needs npm.
package dashboard

import "embed"

//go:embed all:dist
var Assets embed.FS
