// Package migrations 以 go:embed 打包 SQL 迁移文件，使 fp 成为单二进制自迁移。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
