package app

import "embed"

//go:embed assets/app.css assets/app.js
var embeddedAssets embed.FS
