//go:build tools

// Package tools pins the CI tools by go.sum, apart from the runtime's own module, so their dependencies
// never reach it: run them from this directory (go run golang.org/x/vuln/cmd/govulncheck -C .. ./...).
package tools

import _ "golang.org/x/vuln/cmd/govulncheck"
