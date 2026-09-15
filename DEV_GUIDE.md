# sub2api 项目开发指南

> 本文档记录项目环境配置、常见坑点和注意事项，供 Claude Code 和团队成员参考。

## 一、项目基本信息

| 项目 | 说明 |
|------|------|
| **上游仓库** | Wei-Shaw/sub2api |
| **Fork 仓库** | hongheshan-svg/sub2api |
| **技术栈** | Go 后端 (Ent ORM + Gin) + Vue3 前端 (pnpm) |
| **数据库** | PostgreSQL 16 + Redis |
| **包管理** | 后端: go modules, 前端: **pnpm**（不是 npm） |

## 二、本地环境配置

### PostgreSQL 16 (Windows 服务)

| 配置项 | 值 |
|--------|-----|
| 端口 | 5432 |
| psql 路径 | `C:\Program Files\PostgreSQL\16\bin\psql.exe` |
| pg_hba.conf | `C:\Program Files\PostgreSQL\16\data\pg_hba.conf` |
| 数据库凭据 | user=`sub2api`, password=`sub2api`, dbname=`sub2api` |
| 超级用户 | user=`postgres`, password=`postgres` |

### Redis

| 配置项 | 值 |
|--------|-----|
| 端口 | 6379 |
| 密码 | 无 |

### 开发工具

```bash
# golangci-lint（CI 用 v2.13，本地建议装同一版以免版本差异带来的噪音）
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13

# pnpm (前端包管理)
npm install -g pnpm
```

## 三、CI/CD 流水线

### GitHub Actions Workflows

| Workflow | 触发条件 | 检查内容 |
|----------|----------|----------|
| **backend-ci.yml** | push, pull_request | 单元测试 + 集成测试 + golangci-lint v2.13 |
| **security-scan.yml** | push, pull_request, 每周一 | govulncheck + gosec + pnpm audit |
| **release.yml** | tag `v*`，或手动 `workflow_dispatch`（输入已存在的 tag，可选 `simple_release`） | 构建发布（PR 不触发） |

### CI 要求

- Go 版本必须是 **1.27.0**：三个 workflow 都用 `go-version-file: backend/go.mod` 取版本，随后硬断言 `go version | grep -q 'go1.27.0'`。升级 Go 时要同时改 `backend/go.mod`、`backend-ci.yml`（两处）、`release.yml`、`security-scan.yml` 里的这句断言，**以及三个 Dockerfile 里的 Go 构建镜像**（`Dockerfile` / `deploy/Dockerfile` 的 `ARG GOLANG_IMAGE`、`backend/Dockerfile` 的 `FROM golang:`）。前者漏了 CI 会在版本校验步骤直接失败；**后者漏了 CI 不会报，而是等到有人用这些 Dockerfile 构建时才失败**（`go.mod requires go >= X (running Y; GOTOOLCHAIN=local)`）。
- 前端使用 `pnpm install --frozen-lockfile`，必须提交 `pnpm-lock.yaml`

### 本地测试命令

```bash
# 后端单元测试
cd backend && go test -tags=unit ./...

# 后端集成测试
cd backend && go test -tags=integration ./...

# 代码质量检查
cd backend && golangci-lint run ./...

# 前端依赖安装（必须用 pnpm）
cd frontend && pnpm install
```

### 发版流程与 Release Notes 规范

**触发**：推送 `v*` tag 触发 `release.yml`，它会：①从 tag 名取版本号写入 VERSION；②构建前端；③通过 **GoReleaser** 构建多架构镜像推送到 **GHCR**（`ghcr.io/<owner>/sub2api`，Docker Hub 仅在配了 `DOCKERHUB_USERNAME` secret 时才推）；④**GoReleaser 自动创建一个已发布（非草稿）的 GitHub Release**，标题 `Sub2API X.Y.Z`、并附构建产物归档；⑤`sync-version-file` job 自动把 `chore: sync VERSION to X [skip ci]` 提交回 main。**所以发版无需手动改 VERSION、也无需手动创建 Release，只需打 tag。**

也可以在 Actions 页手动 `workflow_dispatch` 触发（无需重新推 tag）：输入已存在的 `tag`（如 `v1.0.0`），可选勾选 `simple_release`（仅构建 x86_64 GHCR 镜像，跳过其余产物）——用于给已打好的 tag 重跑失败的发布，或临时出一个精简镜像。

```bash
# 在已同步的 main 上发版（fork 版本与上游 1:1 对齐，见「坑 12」）
# ⚠️ 必须用 -f：git fetch upstream --tags 会在本地留下指向「上游同名 commit」的
#    v0.1.X tag，普通 git tag -a 会「already exists」静默失败，随后 push 会把
#    上游那个错的 commit 当发版目标。-f 强制把 tag 移到 fork main。
git tag -f -a v0.1.X main -m "v0.1.X"
git push origin v0.1.X --force        # 触发 release.yml
# 校验 tag 指向 fork main（而非上游 commit）：两者必须相等
git ls-remote origin 'refs/tags/v0.1.X^{}' | awk '{print $1}'
git ls-remote origin refs/heads/main    | awk '{print $1}'
```

**`release.yml` 会自动创建已发布的 GitHub Release**（GoReleaser，见 `.goreleaser.yaml` 的 `release:` 段）——但 body 是通用模板（`> AI API Gateway Platform…` + tag 消息 + 安装/文档页脚），标题为 `Sub2API X.Y.Z`。**所以发布后需用 `gh release edit` 把 body 覆盖为规范 Release Notes**（标题保持自动的 `Sub2API X.Y.Z`，不要改成 `gw-link`；“gw-link” 品牌写在 notes 正文开头的引用块里）。

**Release Notes 规范（以 [v0.1.139](https://github.com/hongheshan-svg/sub2api/releases/tag/v0.1.139) 为基准）**：

1. **本仓库改动（fork）** —— 按 `新功能 / 修复优化 / 文档` 分类，每条带 PR 链接。
2. **同步上游** —— 若本次合并了 upstream，**必须附上所同步的 upstream 版本的官方 release notes 原文**（注明上游版本号 + 链接）。取法：
   ```bash
   gh release view v0.1.X --repo Wei-Shaw/sub2api --json name,body --jq '.body'
   ```
3. **安装** —— GHCR `docker pull` 命令。
4. **完整对比** —— `compare/v0.1.<prev>...v0.1.X` 链接。

```bash
# tag 推送、release.yml 跑完后，把自动生成的 Release body 覆盖为规范 notes
# （GoReleaser 已创建好该 Release，所以是 edit 而非 create；不要改标题）
gh release edit v0.1.X --notes-file notes.md
```

> 历史说明：早期 `release.yml` 不创建 Release、需手动 `gh release create --title "gw-link v0.1.X"`；现已改为 GoReleaser 自动创建，故流程改为 `gh release edit` 覆盖 body。

### 发版记录

> 仅记录关键信息，完整 Release Notes 见 [GitHub Releases](https://github.com/hongheshan-svg/sub2api/releases)。新发版在表头下追加一行。

| 版本 | 日期 | 同步上游 | 上游提交 | PR | Merge commit | 冲突处理 |
|------|------|----------|---------|----|--------------|----------|
| v0.3.4 | 2026-09-15 | v0.2.4 → v0.2.5 | 56 | #63, #64(hotfix) | `90e54978c`（重发） | 2 处冲突：`VERSION`（我们 0.3.3 > 上游 0.2.5，取 ours）；`wire_gen.go`（上游把 `ollamaCloudUsageService :=` 上移到 L122 给 `ProvideRateLimitService` 用，保留我们带 `kiroOAuthHandler`+`adminInvoiceHandler` 的 `ProvideAdminHandlers` 调用、删旧位置重复定义）。另修上游新增的 exhaustive `Record<GroupPlatform, KeyGroupProvider>` 缺 fork 的 `kiro` 导致的 TS2741（git 不报冲突，只有 typecheck 抓得到）。**⚠️ 首发版本上线即 crash-loop，tag/release/镜像已撤回重发** —— 见下方事故记录。 |
| v0.1.155 | 2026-07-14 | v0.1.153 → v0.1.155 | 68 | #29 | `e2b5bff8` | 1 处 add/add 去重：`http_upstream_http2_keepalive_test.go`（fork PR #28 的 HTTP/2 keepalive 补丁被上游 cherry-pick #4207 回流，取 ours 复用已有 `timeoutTestPoolSettings()` helper）。`upstream-pr/http2-keepalive` 分支本地+远端已删。 |

**⚠️ v0.3.4 事故记录（首发版本已撤回重发）**：上游 `238_opencode_go_platform.sql` 用**无幂等守卫的裸 DROP+ADD** 重建 `user_platform_quotas.platform` 与 `composite_model_routes.target_platform`，白名单取自上游平台列表，丢掉了 fork 的 `kiro`（234 加入、237 修复时保留）→ 生产库 5 行 `platform='kiro'` 违约 → `ADD CONSTRAINT` 失败 → 迁移中止 → 启动 crash-loop。**与 v0.3.1 的 237 事故完全同型，隔一个上游版本重演**。

- **漏检点**：同步时只 diff 了本次同步窗口（`BASE..upstream/main`，确实 0 个新 migration），没 diff 整个发版区间 `v0.3.3..v0.3.4` —— 238 是上一次合并（上游 0.2.4 期间）带进来的，卡在两次同步的缝里。**以后发版前必须跑 `git diff --name-status <上一个 tag> HEAD -- backend/migrations/`**。
- **修复两处并存**（覆盖互不相交的两类部署）：改 238 补回 kiro（救 crash-loop 的库——失败迁移在事务里回滚、未记账，重启会重跑修复后的内容）；新增 `240_restore_kiro_platform_constraints.sql` 兜底（救表里当时没有 kiro 行、把 kiro-less 238 **成功**应用并记账的库，那类库 238 永不重跑）。240 带幂等守卫，且排在 `238_purge_unlimited_user_platform_quotas.sql` 之后（占位行已清空，ADD CONSTRAINT 不会因存量数据失败）。
- **顺带修掉**：v0.3.1 给 237 注册的 checksum 兼容规则填的两个值与真实文件对不上，三个版本里一直失效。新守卫 `TestMigrationChecksumCompatibilityRulesMatchRealFiles` 又抓出另外 9 条同样失效的规则（均在 `v0.3.3..v0.3.4` 未变、与本次事故无关，列入 `knownStaleChecksumRules` 记账）。
- **守卫必须反向验证**：`TestPlatformCheckMigrationsKeepKiro` 的第一版断言整个文件文本含 `'kiro'`，在 kiro 被抠掉后**照样通过**——因为 238 的注释里就写着 `platform='kiro'`。改为解析 `ADD CONSTRAINT ... CHECK (...)` **子句内部的白名单**后才真正生效。
- **撤回重发流程**：`gh release delete v0.3.4 --yes --cleanup-tag`（连远程 tag 一起删）→ 删本地 tag → `git tag -f -a v0.3.4 main` → `git push --force` → 重发后 GHCR 的 `0.3.4/0.3/0/latest` 四个标签自动移到新镜像、旧的变 untagged 孤儿 → 逐个复查 `tags==[]` 后按 id 删除。⚠️ **删 GHCR 镜像需要 `read:packages,delete:packages` scope**（默认 token 没有，`gh auth refresh -s` 是交互式的，得用户自己跑）。⚠️ **tag 名复用时，已拉过旧镜像的机器必须强制 `docker pull`**，否则用的还是本地缓存的坏镜像。

**v0.3.4 校验记录**：本地 `go build -tags embed` / `go test -tags=unit`（57 包 ok，0 FAIL）/ `go test ./...` / `golangci-lint`（0 issues）/ vue-tsc / eslint `--max-warnings 0` / `pnpm build` / **全量 vitest（289 文件 2323 tests，0 FAIL）** 全绿；双向语义防丢失：正向上游 106 文件 0 丢失、反向 11 文件 0 丢失；PR CI 12 pass / 2 skipping，main CI 6/6 success；`release.yml` 4 job 全绿；GHCR `ghcr.io/hongheshan-svg/sub2api:0.3.4` 多架构 `linux/amd64`+`linux/arm64` ✓。

**v0.1.155 校验记录**：本地 `go build -tags embed` / `go test -tags=unit`（0 FAIL）/ vue-tsc / eslint / critical vitest（6 文件 91 tests）全绿；CI 真跑（test 6m23s、golangci-lint 2m40s、frontend 1m21s）；`release.yml` 4 job 全绿；GHCR `ghcr.io/hongheshan-svg/sub2api:0.1.155` 多架构 `linux/amd64`+`linux/arm64` ✓。

## 四、常见坑点 & 解决方案

### 坑 1：pnpm-lock.yaml 必须同步提交

**问题**：`package.json` 新增依赖后，CI 的 `pnpm install --frozen-lockfile` 失败。

**原因**：上游 CI 使用 pnpm，lock 文件不同步会报错。

**解决**：
```bash
cd frontend
pnpm install  # 更新 pnpm-lock.yaml
git add pnpm-lock.yaml
git commit -m "chore: update pnpm-lock.yaml"
```

---

### 坑 2：npm 和 pnpm 的 node_modules 冲突

**问题**：之前用 npm 装过 `node_modules`，pnpm install 报 `EPERM` 错误。

**解决**：
```bash
cd frontend
rm -rf node_modules  # 或 PowerShell: Remove-Item -Recurse -Force node_modules
pnpm install
```

---

### 坑 3：PowerShell 中 bcrypt hash 的 `$` 被转义

**问题**：bcrypt hash 格式如 `$2a$10$xxx...`，PowerShell 把 `$2a` 当变量解析，导致数据丢失。

**解决**：将 SQL 写入文件，用 `psql -f` 执行：
```bash
# 错误示范（PowerShell 会吃掉 $）
psql -c "INSERT INTO users ... VALUES ('$2a$10$...')"

# 正确做法
echo "INSERT INTO users ... VALUES ('\$2a\$10\$...')" > temp.sql
psql -U sub2api -h 127.0.0.1 -d sub2api -f temp.sql
```

---

### 坑 4：psql 不支持中文路径

**问题**：`psql -f "D:\中文路径\file.sql"` 报错找不到文件。

**解决**：复制到纯英文路径再执行：
```bash
cp "D:\中文路径\file.sql" "C:\temp.sql"
psql -f "C:\temp.sql"
```

---

### 坑 5：PostgreSQL 密码重置流程

**场景**：忘记 PostgreSQL 密码。

**步骤**：
1. 修改 `C:\Program Files\PostgreSQL\16\data\pg_hba.conf`
   ```
   # 将 scram-sha-256 改为 trust
   host    all    all    127.0.0.1/32    trust
   ```
2. 重启 PostgreSQL 服务
   ```powershell
   Restart-Service postgresql-x64-16
   ```
3. 无密码登录并重置
   ```bash
   psql -U postgres -h 127.0.0.1
   ALTER USER sub2api WITH PASSWORD 'sub2api';
   ALTER USER postgres WITH PASSWORD 'postgres';
   ```
4. 改回 `scram-sha-256` 并重启

---

### 坑 6：Go interface 新增方法后 test stub 必须补全

**问题**：给 interface 新增方法后，编译报错 `does not implement interface (missing method XXX)`。

**原因**：所有测试文件中实现该 interface 的 stub/mock 都必须补上新方法。

**解决**：
```bash
# 搜索所有实现该 interface 的 struct
cd backend
grep -r "type.*Stub.*struct" internal/
grep -r "type.*Mock.*struct" internal/

# 逐一补全新方法
```

---

### 坑 7：Windows 上 psql 连 localhost 的 IPv6 问题

**问题**：psql 连 `localhost` 先尝试 IPv6 (::1)，可能报错后再回退 IPv4。

**建议**：直接用 `127.0.0.1` 代替 `localhost`。

---

### 坑 8：Windows 没有 make 命令

**问题**：CI 里用 `make test-unit`，本地 Windows 没有 make。

**解决**：直接用 Makefile 里的原始命令：
```bash
# 代替 make test-unit
go test -tags=unit ./...

# 代替 make test-integration
go test -tags=integration ./...
```

---

### 坑 9：Ent Schema 修改后必须重新生成

**问题**：修改 `ent/schema/*.go` 后，代码不生效。

**解决**：
```bash
cd backend
go generate ./ent  # 重新生成 ent 代码（json.RawMessage 字段会生成为同类型的 jsontext.Value，属预期）
git add ent/       # 生成的文件也要提交
```

---

### 坑 10：前端测试看似正常，但后端调用失败（模型映射被批量误改）

**典型现象**：
- 前端按钮点测看起来正常；
- 实际通过 API/客户端调用时返回 `Service temporarily unavailable` 或提示无可用账号；
- 常见于 OpenAI 账号（例如 Codex 模型）在批量修改后突然不可用。

**根因**：
- OpenAI 账号编辑页默认不显式展示映射规则，容易让人误以为“没映射也没关系”；
- 但在**批量修改同时选中不同平台账号**（OpenAI + Antigravity/Gemini）时，模型白名单/映射可能被跨平台策略覆盖；
- 结果是 OpenAI 账号的关键模型映射丢失或被改坏，后端选不到可用账号。

**修复方案（按优先级）**：
1. **快速修复（推荐）**：在批量修改中补回正确的透传映射（例如 `gpt-5.3-codex -> gpt-5.3-codex-spark`）。
2. **彻底重建**：删除并重新添加全部相关账号（最稳但成本高）。

**关键经验**：
- 如果某模型已被软件内置默认映射覆盖，通常不需要额外再加透传；
- 但当上游模型更新快于本仓库默认映射时，**手动批量添加透传映射**是最简单、最低风险的临时兜底方案；
- 批量操作前尽量按平台分组，不要混选不同平台账号。

---

### 坑 11：PR 提交前检查清单

提交 PR 前务必本地验证：

- [ ] `go test -tags=unit ./...` 通过
- [ ] `go test -tags=integration ./...` 通过
- [ ] `golangci-lint run ./...` 无新增问题
- [ ] `pnpm-lock.yaml` 已同步（如果改了 package.json）
- [ ] 所有 test stub 补全新接口方法（如果改了 interface）
- [ ] Ent 生成的代码已提交（如果改了 schema）

---

### 坑 12：同步上游时 VERSION 冲突 —— 保留 fork 版本，不要降级

**背景**：fork 有自己独立的版本号节奏，且**领先 upstream**。例如 2026-06 同步时 upstream 在 `0.1.136`，而我们 fork 已自己 bump 到 `0.1.138`（merge-base 为 `0.1.135`）。

**问题**：`git merge upstream/main` 时 `backend/cmd/server/VERSION` 必然冲突（两边都改过同一行）。

**解决**：**保留 HEAD（fork）的较高版本，不要选 upstream 的较低版本** —— 否则等于把 fork 的版本号降级。

```bash
# VERSION 冲突时：保留 fork 当前值，而不是 upstream 的
printf '0.1.138\n' > backend/cmd/server/VERSION
git add backend/cmd/server/VERSION
```

**同步上游时的典型冲突集**（均为 fork 附加功能 vs upstream 新增的"两边都加行"机械冲突，保留双方即可）：

- `backend/internal/handler/handler.go` / `wire.go`：保留 fork 的 handler（如 `Invoice`）**＋** upstream 新增的（如 `Compliance`）
- `backend/cmd/server/wire_gen.go`：手动保留双方；**不要用 `go generate` / `wire gen` 重新生成**（invoice 的 `NotificationService` 不是注册 provider，会报 `no provider found for *service.NotificationService`），改用 `go build ./...` 验证编译通过即可
- 前端若两边对同一处做了等价重构（如 `bedrock_cc_compat` 改顶层 bool），取 fork 版

**相关标签坑**：fork 自己的 `vX.Y.Z` release tag 可能和 upstream 的同名标签撞名（指向不同提交）。`git fetch upstream --tags` 会提示 `would clobber existing tag`，这是预期现象，**不要用 `--force` 覆盖 fork 自己的发布标签**。

## 五、常用命令速查

### 数据库操作

```bash
# 连接数据库
psql -U sub2api -h 127.0.0.1 -d sub2api

# 查看所有用户
psql -U postgres -h 127.0.0.1 -c "\du"

# 查看所有数据库
psql -U postgres -h 127.0.0.1 -c "\l"

# 执行 SQL 文件
psql -U sub2api -h 127.0.0.1 -d sub2api -f migration.sql
```

### Git 操作

```bash
# 同步上游（会有冲突，VERSION 等的处理见「坑 12」）
git fetch upstream
git checkout main
git merge upstream/main
# 解决冲突：VERSION 保留 fork 较高版本；handler/wire/wire_gen 保留双方；go build ./... 验证
# 落地走 PR（不直接 push 到共享 main）：
git switch -c feat/sync-upstream-<ver>
git push -u origin feat/sync-upstream-<ver>
gh pr create --base main

# 创建功能分支
git checkout -b feature/xxx

# Rebase 到最新 main
git fetch upstream
git rebase upstream/main
```

### 前端操作

```bash
# 安装依赖（必须用 pnpm）
cd frontend
pnpm install

# 开发服务器
pnpm dev

# 构建
pnpm build
```

### 后端操作

```bash
# 运行服务器
cd backend
go run ./cmd/server/

# 生成 Ent 代码
go generate ./ent

# 运行测试
go test -tags=unit ./...
go test -tags=integration ./...

# Lint 检查
golangci-lint run ./...
```

## 六、项目结构速览

```
sub2api/
├── backend/
│   ├── cmd/server/          # 主程序入口
│   ├── ent/                 # Ent ORM 生成代码
│   │   └── schema/          # 数据库 Schema 定义
│   ├── internal/
│   │   ├── handler/         # HTTP 处理器
│   │   ├── service/         # 业务逻辑
│   │   ├── repository/      # 数据访问层
│   │   └── server/          # 服务器配置
│   ├── migrations/          # 数据库迁移脚本
│   └── config.yaml          # 配置文件
├── frontend/
│   ├── src/
│   │   ├── api/             # API 调用
│   │   ├── components/      # Vue 组件
│   │   ├── views/           # 页面视图
│   │   ├── types/           # TypeScript 类型
│   │   └── i18n/            # 国际化
│   ├── package.json         # 依赖配置
│   └── pnpm-lock.yaml       # pnpm 锁文件（必须提交）
├── DEV_GUIDE.md             # 本文档
├── CLAUDE.md                # Claude Code 指南（gitignored，本地文件）
└── AGENTS.md                # Codex 指南（gitignored，本地文件）
```

## 七、参考资源

- [上游仓库](https://github.com/Wei-Shaw/sub2api)
- [Ent 文档](https://entgo.io/docs/getting-started)
- [Vue3 文档](https://vuejs.org/)
- [pnpm 文档](https://pnpm.io/)
