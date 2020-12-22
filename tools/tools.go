// +build tools

package tools

import (
	_ "github.com/jstemmer/go-junit-report"
	_ "golang.org/x/lint/golint"
	_ "golang.org/x/tools/cmd/goimports"
	_ "honnef.co/go/tools/cmd/staticcheck"
)
