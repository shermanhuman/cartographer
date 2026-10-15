// Package web embeds the browser UI served by `cartographer serve`.
package web

import "embed"

// FS holds index.html, the ES modules, styles and icons (with their license).
//
//go:embed index.html *.js *.css assets ICON-LICENSE.txt ICON-NOTICE.txt
var FS embed.FS
