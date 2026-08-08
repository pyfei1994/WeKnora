# WeKnora 定制分支说明（FoxMe）

本仓库是腾讯官方 [Tencent/WeKnora](https://github.com/Tencent/WeKnora) 的 fork，
用于在官方能力之上叠加 FoxMe 自己的小定制，同时保留随时同步官方更新的能力。

---

## 1. 版本基线

| 项 | 值 |
| --- | --- |
| 官方基线 tag | `v0.7.2` |
| 官方基线 commit | `3d5d8bfc` |
| 定制分支 | `feat/builtin-toggle` |
| 定制分支顶部 commit | `22d864ff`（含 5 个定制 commit，见 2.3） |
| fork 远端 | `origin` = `pyfei1994/WeKnora` |
| 上游远端 | `upstream` = `Tencent/WeKnora` |

> 同步官方更新一律用**版本 tag**（如 `v0.7.2`），不用 `main` 分支：
> tag 更稳、可复现、冲突可控，且与镜像版本号对齐。

---

## 2. 定制内容

### 2.1 后端（`internal/handler/model.go`）
- 在 `CreateModelRequest` / `UpdateModelRequest` 增加 `is_builtin *bool` 字段。
- 仅 **SystemAdmin（platform key）** 可将其设为 `true`；租户管理员尝试会被 `403 Forbidden`
  （防止越权把自己的模型变成全租户可见）。
- 设为内置时：
  - `model.IsBuiltin = true`
  - `model.ManagedBy = "foxme"`（区别于官方 YAML 导入的 `managed_by='yaml'`）
- 模型可见性沿用官方逻辑：`WHERE tenant_id=? OR is_builtin=true`。

### 2.2 前端（`frontend/`）
- `src/components/ModelEditorDialog.vue`：在抽屉表单新增「可见性」分区
  （仅 SystemAdmin 可见），含 `是否内置模型` 开关 `is_builtin`。
- `src/views/settings/ModelSettings.vue`：保存模型时透传 `is_builtin`。
- `src/i18n/locales/zh-CN.ts` / `en-US.ts`：新增 3 个 i18n key
  （`sectionVisibility` / `isBuiltinLabel` / `isBuiltinDesc`）。

> 这样 FoxMe 中台用 platform key 调 API 创建模型时，模型自动成为平台级内置模型，
> 对所有租户（工作空间）可见、可用、不可改，契合「中台统一下发默认模型」的设计。

### 2.3 深层修复（让 is_builtin 开关真正可用，5 个 commit）

**为什么会有这些改动？** 官方 API **刻意不开放**运行时修改内置模型的能力（见 2.4 设计哲学），
且官方代码自身还有两处疏漏。FoxMe 需要「中台用 API 动态把模型提升/降级为内置」，
所以 fork 里把这些一并补齐。

| commit | 文件 | 解决什么 |
| --- | --- | --- |
| `9c59e317` / `8ae36aac` | handler/model.go + 前端抽屉 + i18n | 加 `is_builtin` 字段与开关（2.1/2.2） |
| `85323c8c` | CUSTOMIZATIONS.md + docker-compose.foxme.yml | 文档与部署覆盖 |
| `f6e93a76` | `internal/application/repository/model.go:Update` | **官方疏漏**：GORM `.Select("*").Updates(struct)` 仍跳过零值字段（bool false / 空字符串），导致 `is_builtin=false` 写不进去。追加 `Updates(map[string]interface{}{...})` 强制写（仿官方 `datasource_repo.go:85` 的既有范式） |
| `5efbfdc2` | `internal/application/service/model.go:UpdateModel` | 放开 demotion：官方硬编码 `model.IsBuiltin = true`（内置模型永不降级），改为 SystemAdmin 显式传 `is_builtin=false` 时尊重请求；`managed_by` 仍清空防 YAML reconciler 抢回 |
| `5efbfdc2` | `internal/middleware/auth.go:platformAPIKeyIdentity` | platform key 合成用户设 `IsSystemAdmin=true`（对齐「platform 即超管」语义） |
| `e2f70095` | `internal/middleware/auth.go:attachPlatformAPIKeyAuthContext` | **官方疏漏**：authSession 漏传 `SystemAdmin` 字段，导致 `IsSystemAdminFromContext` 恒为 false |
| `22d864ff` | `internal/middleware/auth.go:attachAPIKeyAuthContext` | **官方疏漏（关键）**：带 `X-Tenant-ID` 时 platform key 走的是这条路径而非上一条，同样漏传 `SystemAdmin`。不修这条，前面的改动全部无效 |

**端到端验证（platform key + X-Tenant-ID: 10000）**：
```
PUT /api/v1/models/{id}  is_builtin=true  → HTTP 200, is_builtin=true  ✅
PUT /api/v1/models/{id}  is_builtin=false → HTTP 200, is_builtin=false ✅
```

### 2.4 官方为什么不开放 API 改内置模型（设计哲学，rebase 时勿当 bug 还原）

1. **内置模型 = 平台基础设施，不是租户业务数据**：来源是 YAML 配置（`managed_by='yaml'`），
   启动 reconciler 灌库。官方认为其增删改应走「改配置 + 重载」的声明式、可审计、可复现路径。
2. **防误操作，影响面是全租户**：内置模型对所有租户可见，改坏一处全平台遭殃。
   两道闸：非 SystemAdmin 不能动；SystemAdmin 也只能改参数不能降级。
3. **公开 API 面向租户业务**：内置模型属平台运维范畴，官方收敛到「管理员 + 配置文件」，
   不放完整可写能力进公开 API。

> FoxMe 的多租户运营场景（中台批量动态管理内置模型）超出官方预设，故定制放开。
> **官方更新时若该处逻辑变化，注意保留这三处定制，不要被上游覆盖。**

---

## 3. 镜像（阿里云私有镜像库）

镜像库：`registry.cn-shanghai.aliyuncs.com/eftik/`

| 服务 | 镜像 | 当前 tag | Registry Digest |
| --- | --- | --- | --- |
| UI | `foxme-weknora-ui` | `0.7.2-foxme` / `latest` | `sha256:eb1e374c2312a5c56bb6ff6d730897f0af2d118a11767ffa4b5ab67162ccc994` |
| App | `foxme-weknora-app` | `0.7.2-foxme` / `latest` | `sha256:23fe9a537f179bbf435f6eb98cb2fe48efd04814cdb68ce13eebd0704372a95e` |
| DocReader | `foxme-weknora-docreader` | `0.7.2-foxme` / `latest` | `sha256:1f520c9e7c5890c6a97997523e536e9e312065548aeed826eb8ef600ce988ba9` |

> sandbox 镜像未重建，部署时继续用官方 `wechatopenai/weknora-sandbox`。

### 3.1 部署
见同目录 `docker-compose.foxme.yml`（官方 compose 的非破坏性覆盖）：

```bash
docker compose -f docker-compose.yml -f docker-compose.foxme.yml up -d
```

不要加 `--build`，镜像直接拉取。

---

## 4. 如何同步官方更新

```bash
# 1. 拉上游
git fetch upstream --tags

# 2. 在定制分支上 rebase 到新的官方 tag（例如 v0.7.3）
git checkout feat/builtin-toggle
git rebase v0.7.3          # 切勿在管道里跑 rebase（见第 6 节踩坑）

# 3. 解决冲突（通常只有 i18n 文件，手动补回 3 个 key 即可）
# 4. 重新打镜像并推送（见第 5 节）
# 5. 更新 docker-compose.foxme.yml 里的三个 tag
```

> ⚠️ 官方 v0.7.1→v0.7.2 没有改动 `model.go` / 模型编辑相关前端文件，
> 仅重写了 i18n，所以本次 rebase 零冲突，i18n 手动补 3 个 key 即可。

---

## 5. 如何重新打镜像

```bash
REG=registry.cn-shanghai.aliyuncs.com/eftik
VER=0.7.2-foxme

# DocReader（v0.7.2 源码与官方一致，直接 re-tag 官方 latest 即可；如改动则重新 build）
docker pull wechatopenai/weknora-docreader:latest
docker tag  wechatopenai/weknora-docreader:latest $REG/foxme-weknora-docreader:$VER
docker push $REG/foxme-weknora-docreader:$VER

# App
docker build -f docker/Dockerfile.app \
  --build-arg GOPROXY_ARG=https://goproxy.cn,direct \
  --build-arg GOSUMDB_ARG=off \
  --build-arg APK_MIRROR_ARG=mirrors.tuna.tsinghua.edu.cn \
  --build-arg GO_VERSION_ARG=1.26 \
  --build-arg VERSION_ARG=$VER \
  -t $REG/foxme-weknora-app:$VER .
docker push $REG/foxme-weknora-app:$VER

# UI（先产出 dist）
cd frontend
npm install --registry https://registry.npmmirror.com   # 用 install 而非 ci，避免批量删除触发安全钩子
npm run build
cd ..
docker build -f frontend/Dockerfile -t $REG/foxme-weknora-ui:$VER ./frontend
docker push $REG/foxme-weknora-ui:$VER

# 同时打 latest
for s in app ui docreader; do
  docker tag $REG/foxme-weknora-$s:$VER $REG/foxme-weknora-$s:latest
  docker push $REG/foxme-weknora-$s:latest
done
```

---

## 6. 踩坑记录

1. **git rebase 不要放进管道**：`git rebase ... | tail` 可能因 SIGPIPE 被杀，导致
   `.git/refs/heads/` 被删、对象丢失、仓库损坏。rebase 前先备份改动文件。
2. **UI 构建用 `npm install` 而非 `npm ci`**：`npm ci` 会触发批量删除，
   超过 WorkBuddy 安全钩子阈值（50 文件）而失败，导致 `dist` 未更新、`docker COPY` 用到旧缓存。
3. **App 构建 apt 502**：注入 `APK_MIRROR_ARG=mirrors.tuna.tsinghua.edu.cn`，
   避开 `deb.debian.org`（Fastly）无限重试。
4. **拉大镜像走国内 mirror**：本机 Docker 的 mirror 未生效、叠加代理会导致
   `unexpected EOF`。可带 registry 前缀直拉再 `docker tag`（如 `docker.m.daocloud.io/...`）。
5. **GORM `.Select("*").Updates(struct)` 依然跳过零值字段**（bool false / 空字符串）：
   需要持久化零值时必须用 `Updates(map[string]interface{}{...})`，参考官方
   `internal/repository/datasource_repo.go` 的注释「Updates(struct) skips zero values,
   so use a map here to persist cleared error」。
6. **`applyAuthSession` 用 `authSession.SystemAdmin` 字段写 ctx，不会从 user 自动复制**：
   每个构造 `authSession` 的地方都必须显式 `SystemAdmin: user.IsSystemAdmin`，
   否则 `IsSystemAdminFromContext` 恒为 false。官方 JWT 路径写了，但两条 API key 路径都漏了。
7. **platform key 有两条 dispatch 路径**：无 `X-Tenant-ID` 走
   `attachPlatformAPIKeyAuthContext`；带 `X-Tenant-ID` 走 `attachAPIKeyAuthContext`。
   改鉴权/身份逻辑时必须两条都改，否则「无头时好、带头时坏」。
8. **本机 Git Bash 的 native `ssh-keygen` 不认 `/c/...` POSIX 路径**（报
   "Saving key failed: No such file or directory"），必须用 Windows 风格
   `C:/Users/vante/.ssh/id_ed25519`。
