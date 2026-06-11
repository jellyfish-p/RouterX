package main

import (
	"io"
	"os"

	"ariga.io/atlas-provider-gorm/gormschema"
	"routerx/internal/model"
)

// Atlas GORM Loader
// 用于将 GORM 模型转换为 Atlas HCL schema，
// 配合 atlas.hcl 和 atlas migrate diff 命令自动生成迁移 SQL。
//
// 用法:
//
//	go run cmd/migrate/main.go > schema.hcl
//	atlas migrate diff <name> --dev-url "sqlite://file?mode=memory" --to file://schema.hcl --dir file://migrations/<dialect>
//
// 示例 (PostgreSQL):
//
//	go run cmd/migrate/main.go > schema.hcl
//	atlas migrate diff add_channel_weight --dev-url "sqlite://file?mode=memory&_fk=1" --to file://schema.hcl --dir file://migrations/postgres
func main() {
	stmts, err := gormschema.New("postgres").Load(
		&model.User{},
		&model.UserIdentity{},
		&model.Group{},
		&model.Token{},
		&model.Channel{},
		&model.Log{},
		&model.RedemCode{},
		&model.Setting{},
	)
	if err != nil {
		os.Stderr.WriteString(err.Error())
		os.Exit(1)
	}
	io.WriteString(os.Stdout, stmts)
}
