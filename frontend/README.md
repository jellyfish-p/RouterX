# RouterX Frontend

RouterX 前端基于 Nuxt 4、Vue 3、Nuxt UI 和 Pinia 构建，提供系统初始化、登录、用户控制台和管理后台页面。

## 开发要求

前端只使用 Bun。请不要使用 npm、pnpm 或 yarn 安装依赖或运行脚本。

```bash
bun install
bun run dev
```

开发服务默认运行在：

```text
http://localhost:5173
```

开发环境会将 `/v0`、`/v1` 和 `/health` 代理到后端：

```text
http://localhost:3000
```

## 常用命令

```bash
# 安装依赖
bun install

# 启动开发服务
bun run dev

# 构建生产产物
bun run build

# 本地预览生产产物
bun run preview

# 类型检查
bun run typecheck

# 代码检查
bun run lint
```
