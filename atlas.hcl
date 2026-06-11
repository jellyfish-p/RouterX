// Atlas configuration for RouterX
// 使用 GORM Provider 从 Go 模型自动加载 schema

data "external_schema" "gorm" {
  program = ["go", "run", "cmd/migrate/main.go"]
}

env "gorm" {
  src = data.external_schema.gorm.url

  migration {
    dir = "file://internal/migrate/postgres"
    format = golang_migrate
  }

  # 开发验证用 — SQLite 内存数据库，不污染本地数据
  dev = "sqlite://file?mode=memory&_fk=1"
}
