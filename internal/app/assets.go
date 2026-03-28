package app

import "embed"

//go:embed assets/app.css assets/htmx.min.js
var embeddedAssets embed.FS
