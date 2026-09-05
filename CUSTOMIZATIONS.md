# WeKnora 定制分支说明（KitsuMe）

本仓库是腾讯官方 [Tencent/WeKnora](https://github.com/Tencent/WeKnora) 的 fork，
用于在官方能力之上叠加 KitsuMe（狐分身）的定制开发，同时保留随时同步官方更新的能力。

> **⚠️ 登记规范（必读）**：以后凡是 KitsuMe 在本仓库的**任何额外定制开发**（新增接口、改官方逻辑、
> 改 Dockerfile/nginx/compose、加依赖等），合并进定制分支后**必须回到本文件登记**：
> 在「2. 定制一览表」加一行，并按复杂度在「3. 定制详情」补一小节（动机 / 改动点 / commit /
> 同步上游时的注意事项）。这是为了每次跟官方新版本合并时能一眼看清我们改过什么、哪些冲突要手工保住。
> 历史教训：v0.8.0 合并时就靠这份清单逐条核对了 5 处冲突的取舍。

---

## 1. 版本基线

| 项 | 值 |
| --- | --- |
| 当前官方基线 tag | `v0.8.0`（commit `1edcd54b`） |
| 定制分支 | `feature/v0.8.0-kitsume` |
| 分支顶部 commit | `cd788267` |
| 合并方式 | `git merge v0.8.0`（**merge 而非 rebase**，保留定制提交的 sha 便于核对） |
| 历史基线 | v0.7.2（`feat/builtin-toggle` 时代，已由 v0.8.0 分支取代） |
| fork 远端 | `origin` = `pyfei1994/WeKnora` |
| 上游远端 | `upstream` = `Tencent/WeKnora` |
| 消费方 | KitsuMe 后端（`C:\workplace\kitsume\server`）通过 HTTP API + platform key 调用本服务 |

> 同步官方更新一律用**版本 tag**（如 `v0.8.0`），不用 `main` 分支：
> tag 更稳、可复现、冲突可控，且与镜像版本号对齐。

---

## 2. 定制一览表（速查）

| # | 模块 | 定制内容 | commit | 详见 |
| --- | --- | --- | --- | --- |
| 1 | 模型管理 | `is_builtin` 内置模型：创建/更新 API 支持提升/降级，前端开关 | `9c59e317` `8ae36aac` | 3.1 |
| 2 | 模型仓库 | GORM 零值修复：`is_builtin=false` / `managed_by=''` 能真正落库 | `f6e93a76` | 3.1 |
| 3 | 鉴权 | platform key 合成用户贯通 `IsSystemAdmin`（两条 API key 路径都改） | `5efbfdc2` `e2f70095` `22d864ff` | 3.2 |
| 4 | 用户管理 | system-admin 特权用户 API（建用户/禁启用/重置密码，platform key 专用） | `ff703bcf` | 3.3 |
| 5 | Swagger | Dockerfile.app 构建期再生成 swagger，保证 /swagger 与代码同步 | `ff703bcf` | 3.3 |
| 6 | nginx | `chunked_transfer_encoding on`，修复 gzip JSON 响应挂死 + 保住 SSE | `b2aec009` | 3.4 |
| 7 | query-understand | 图片描述为空时用独立 VLM 调用补图，非视觉主模型也能"看到"图 | `d6324c3c` | 3.5 |
| 8 | 资源目录 | `storage://` 后端 scope 解包，修复聊天回答配图丢失 | `a416ea3c` | 3.6 |
| 9 | Docker | `docker-compose.kitsume.yml` 部署覆盖；本地 dev 暴露 pg/redis 端口 | `eaf1dc22` `7a1efbb6` `ff703bcf` | 3.7 |
| 10 | Docker | 国内构建链：GOPROXY / APK 阿里镜像 / PyPI 清华镜像（PIP_INDEX_URL） | `cd788267` | 3.7 |
| 11 | docreader | 引擎自动选择修复 —— **v0.8.0 上游已用类型级默认引擎表覆盖，合并时取上游版** | `ff2db17e`（已吸收） | 3.8 |
| 12 | 合并 | v0.8.0 merge，5 处冲突手工解决，核心定制零丢失 | `44de91d4` | 3.9 |

> 注：代码里 `managed_by='foxme'`、镜像名前缀等历史命名保留 foxme 字样，
> 是**数据兼容 + 避免无谓迁移**的刻意选择，品牌已统一为 KitsuMe，不再重命名。

---

## 3. 定制详情

### 3.1 is_builtin 内置模型（中台统一下发默认模型）

**文件**：`internal/handler/model.go`、`internal/application/repository/model.go`、
`internal/application/service/model.go`、前端 `ModelEditorDialog.vue` / `ModelSettings.vue` + i18n。

- `CreateModelRequest` / `UpdateModelRequest` 增加 `is_builtin *bool`；仅 **SystemAdmin（platform key）**
  可设为 `true`，租户管理员尝试返回 403（防越权把私有模型变全租户可见）。
- 设为内置时 `model.ManagedBy = "foxme"`（区别于官方 YAML 导入的 `managed_by='yaml'`）。
- **官方疏漏修复**：GORM `.Select("*").Updates(struct)` 跳过零值字段，`is_builtin=false` 写不进去，
  改用 `Updates(map[string]interface{}{...})` 强制写（`f6e93a76`）。
- **放开降级**：官方硬编码 `model.IsBuiltin = true`（内置永不降级），改为 SystemAdmin 显式传
  `is_builtin=false` 时尊重请求；`managed_by` 仍清空防 YAML reconciler 抢回（`5efbfdc2`）。
- 模型可见性沿用官方逻辑：`WHERE tenant_id=? OR is_builtin=true`。

**官方为什么不开放 API 改内置模型（设计哲学，同步上游时勿当 bug 还原）**：

1. 内置模型 = 平台基础设施，官方走「YAML 配置 + 启动 reconciler 灌库」的声明式路径。
2. 防误操作：内置模型全租户可见，改坏一处全平台遭殃。
3. 公开 API 面向租户业务，内置模型属平台运维范畴。

> KitsuMe 的多租户运营场景（中台用 platform key 动态管理内置模型）超出官方预设，故定制放开。
> **同步上游若该处逻辑变化，务必保住 3.1/3.2 的全部改动。**

端到端验证（platform key + X-Tenant-ID）：`PUT /api/v1/models/{id}` 设 `is_builtin=true/false` 均 200 且落库正确。

### 3.2 platform key = SystemAdmin 贯通（3 个 commit，缺一不可）

| commit | 路径 | 解决什么 |
| --- | --- | --- |
| `5efbfdc2` | `internal/middleware/auth.go:platformAPIKeyIdentity` | platform key 合成用户设 `IsSystemAdmin=true` |
| `e2f70095` | `auth.go:attachPlatformAPIKeyAuthContext` | **官方疏漏**：authSession 漏传 `SystemAdmin` 字段 |
| `22d864ff` | `auth.go:attachAPIKeyAuthContext` | **官方疏漏（关键）**：带 `X-Tenant-ID` 时走的是这条路径，同样漏传 |

> 踩坑要点：platform key 有**两条 dispatch 路径**（无 `X-Tenant-ID` / 有 `X-Tenant-ID`），
> 改鉴权/身份逻辑必须两条都改，否则「无头时好、带头时坏」。
> `applyAuthSession` 只认 `authSession.SystemAdmin` 字段，不会从 user 自动复制。

### 3.3 system-admin 特权用户 API（`ff703bcf`）

KitsuMe 中台需要管理终端用户账号，但官方只有 invite_only 注册 + 无管理 API，故新增
（`internal/handler/system.go` + `routes_auth_tenant.go`，路由组 `/system/admin/users`）：

| 接口 | 说明 |
| --- | --- |
| `POST /api/v1/users` | platform key 直接创建用户 + 个人租户，**绕过 invite_only 注册门禁** |
| `POST /api/v1/users/status` | 按邮箱启用/禁用账号并吊销全部 token（禁用即时生效，租户/KB 数据保留） |
| `POST /api/v1/users/reset-password` | 重置密码（原实现带 platform key 调用会撞 default-deny，已修复） |

- 三个路由均注册 `apiKeyPlatform(types.APIKeyCapabilitySystemTenantsManage)` 能力声明。
- **刻意不提供 `GET /users`**（最小暴露面；KitsuMe 客户端全部用 POST 语义调用）。
  ⚠️ v0.8.0 合并后曾误以 `GET /system/admin/users` 404 怀疑丢路由——fork 本就没有 GET，不是合并问题。
- 同 commit 顺带：`docker-compose.yml` 暴露 postgres 5432 / redis 6379 到宿主（本地开发客户端直连用）；
  `docker/Dockerfile.app` 构建期重新生成 swagger。
- v0.8.0 上游同文件新增了 `CreateSystemUser`（服务端生成密码）路由 `/system/admin/users/create`，
  合并时**两者共存保留**。

### 3.4 nginx：chunked_transfer_encoding on（`b2aec009`）

官方 `frontend/nginx.conf` 关闭了 chunked，导致 gzip 压缩的 JSON 响应（>1024B）既无 Content-Length
（proxy_buffering off）也无 chunked 帧，浏览器收不到响应结束：登录 POST 返 200 但 axios 挂到超时。
改为 `chunked_transfer_encoding on`（HTTP/1.1 流式标准，也是 SSE 所需）。
**同步上游覆盖 nginx.conf 时必须保留这行**（含 fork 的内联 proxy/SSE 配置整体保留）。

### 3.5 query-understand：VLM 独立补图（`d6324c3c`）

「rewrite + intent + image_description 合并一次调用」在视觉模型自由作答时经常不输出结构化的
`image_description` 字段，下游非视觉主模型对图片失明（分身回答「没收到图片」）。
定制：当 image_description 为空且消息含图时，用一次**独立 VLM 调用**补齐图片描述。
**同步上游注意 `internal/application/service/` 下 query-understand 相关改动，保住该补图逻辑。**

### 3.6 资源目录：storage:// 解包（`a416ea3c`）

`resource://` 引用经默认 file service 解析后可能指向 backend-scoped 路径
（`storage://{backendID}/oss://...`），而底层 provider driver（parseOssFilePath）只认 `oss://`，
导致 `resource_urls=public` 重写报 "invalid OSS file path"、聊天回答里图片被丢弃。
定制：在 Catalog GetFile/GetFileURL 中先解包 scope 前缀再委托内层 driver
（对齐 `registerChatLocalImageResolver` 的既有处理）。

### 3.7 Docker / 构建链定制

| 项 | 说明 |
| --- | --- |
| `docker-compose.kitsume.yml` | 部署覆盖：三个服务换成 `registry.cn-shanghai.aliyuncs.com/eftik/kitsume-weknora-{ui,app,docreader}`，tag 为 `0.8.0-kitsume`；pg/redis 端口映射由 base compose 提供，**不要重复映射**（compose 追加合并成两条冲突端口会绑定失败） |
| `docker/Dockerfile.app` | WITH_ANYDOC=1（v0.8.0 Rust 引擎）+ 构建期 swagger 再生成 + `PIP_INDEX_URL`（默认清华 TUNA，海外构建传 `--build-arg PIP_INDEX_URL=` 覆盖回官方源） |
| 国内构建参数模板 | `--provenance=false --build-arg HTTP_PROXY= HTTPS_PROXY= GOPROXY_ARG=https://goproxy.cn,direct APK_MIRROR_ARG=mirrors.aliyun.com WITH_ANYDOC=1`；**APK_MIRROR_ARG 只传主机名**（sed 只换主机名，带 `http://` 会变 `http://http//`） |

### 3.8 docreader 引擎自动选择（已被上游吸收）

原问题：`get_parser_class` 只在请求引擎（或空默认）处理不了该文件类型时回退 builtin，
而 builtin 不注册 pptx/ppt，聊天附件 PowerPoint 报 "Unsupported file type"。
fork 曾用启发式修复（`ff2db17e`）；**v0.8.0 上游已引入 `_DEFAULT_ENGINE_BY_TYPE` 类型级默认引擎表**，
合并时 registry.py 取上游版，fork 补丁不再单独存在。登记在此仅为追溯与防误还原。

### 3.9 v0.8.0 合并冲突解决记录（`44de91d4`，5 处）

| 文件 | 取舍 |
| --- | --- |
| `docker/Dockerfile.app` | v0.8.0 anydoc 构建（WITH_ANYDOC/Rust）+ fork 的 swag swagger 再生成，两者保留 |
| `docreader/parser/registry.py` | 取 v0.8.0 版（类型级默认引擎表，见 3.8） |
| `frontend/nginx.conf` | 保留 fork 内联 proxy 配置 + `chunked_transfer_encoding on`（见 3.4） |
| `internal/handler/system.go` | fork `CreateUser`/`SetUserActive` 与上游 `CreateSystemUser` 共存 |
| `internal/router/routes_auth_tenant.go` | 保留 fork 三个 `apiKeyRoute`（users / users/status / users/reset-password）+ 追加上游 `/users/create` |

合并后容器内全量编译 + 单测通过（Linux 容器；sqlite-vec 需 `apt-get install libsqlite3-dev`；
`TestDeploymentCapabilityKeysMatchFrontend` 在 Windows 工作树因 CRLF 检出假失败，LF 化后 PASS）。

---

## 4. 镜像（阿里云私有镜像库）

镜像库：`registry.cn-shanghai.aliyuncs.com/eftik/`

| 服务 | 镜像 | 当前 tag |
| --- | --- | --- |
| UI | `kitsume-weknora-ui` | `0.8.0-kitsume` |
| App | `kitsume-weknora-app` | `0.8.0-kitsume` |
| DocReader | `kitsume-weknora-docreader` | `0.8.0-kitsume` |

> sandbox 镜像未重建，部署时继续用官方 `wechatopenai/weknora-sandbox`。

### 4.1 部署

见仓库根目录 `docker-compose.kitsume.yml`（官方 compose 的非破坏性覆盖）：

```bash
docker compose -f docker-compose.yml -f docker-compose.kitsume.yml up -d
```

不要加 `--build`，镜像直接拉取。升级版本只需把覆盖文件里三个 tag 换成新的 `x.y.z-kitsume`。

### 4.2 重新打镜像

```bash
REG=registry.cn-shanghai.aliyuncs.com/eftik
VER=0.8.0-kitsume

# App（--provenance=false 必加，否则 ACR 拒收 OCI 空清单 attestation）
docker build -f docker/Dockerfile.app --provenance=false \
  --build-arg HTTP_PROXY= --build-arg HTTPS_PROXY= \
  --build-arg GOPROXY_ARG=https://goproxy.cn,direct \
  --build-arg APK_MIRROR_ARG=mirrors.aliyun.com \
  --build-arg WITH_ANYDOC=1 \
  --build-arg VERSION_ARG=$VER \
  -t $REG/kitsume-weknora-app:$VER .
docker push $REG/kitsume-weknora-app:$VER

# UI（先产出 dist）
cd frontend
npm install --registry https://registry.npmmirror.com   # 用 install 而非 ci（见第 7 节踩坑 2）
npm run build
cd ..
docker build -f frontend/Dockerfile --provenance=false -t $REG/kitsume-weknora-ui:$VER ./frontend
docker push $REG/kitsume-weknora-ui:$VER

# DocReader
docker build -f docker/Dockerfile.docreader --provenance=false \
  --build-arg APK_MIRROR_ARG=mirrors.aliyun.com \
  -t $REG/kitsume-weknora-docreader:$VER .
docker push $REG/kitsume-weknora-docreader:$VER
```

> ACR 推送报 `unknown manifest class for application/vnd.oci.empty.v1+json` 时：
> app/ui 用 `--provenance=false` 重建即可；docreader 若重建后仍报错（containerd store 保留
> attestation），用「FROM 本地基底 + LABEL」空重建生成纯净清单再推。

---

## 5. 如何同步官方更新

```bash
# 1. 拉上游
git fetch upstream --tags

# 2. 在定制分支上 merge 新的官方 tag（保持 merge 风格，与 v0.8.0 一致）
git checkout feature/v0.8.0-kitsume
git merge v0.8.1

# 3. 按第 2 节「定制一览表」逐条核对冲突取舍（重点：3.1/3.2/3.3/3.4/3.5）
# 4. Linux 容器内编译 + 单测验证
# 5. 重新打镜像并推送（第 4.2 节），更新 docker-compose.kitsume.yml 三个 tag
# 6. 更新本文件第 1 节版本基线 + 新增一条 3.x 合并记录
```

---

## 6. 如何登记新定制（规范）

每次在定制分支合入新的 KitsuMe 定制后：

1. **「2. 定制一览表」加一行**：模块、一句话说明、commit、详情节号。
2. **复杂定制在「3. 定制详情」加小节**，至少写清：
   - 动机（官方为什么做不到 / KitsuMe 需要什么）
   - 改动点（文件 + 行为变化）
   - 同步上游时的注意事项（哪些行为是刻意的，别当 bug 还原）
3. 若新定制影响合并冲突面（改了官方文件），在最近的「3.x 合并记录」风格上留一笔即可，后续合并照单核对。
4. 不允许出现「改了代码但本文件无记录」的定制——每次官方升级，这份文件就是核对清单。

---

## 7. 踩坑记录

1. **git rebase 不要放进管道**：`git rebase ... | tail` 可能因 SIGPIPE 被杀，导致
   `.git/refs/heads/` 被删、对象丢失、仓库损坏。rebase/merge 前先备份改动文件。
2. **UI 构建用 `npm install` 而非 `npm ci`**：`npm ci` 会触发批量删除 node_modules，
   超过 WorkBuddy 安全钩子阈值（50 文件）而失败，导致 `dist` 未更新、`docker COPY` 用到旧缓存；
   同理 vite `emptyOutDir` 清 dist 被拦时手动 `rm -rf dist`。
3. **App 构建 apt 失败**：注入 `APK_MIRROR_ARG=mirrors.aliyun.com`（只传主机名！带 `http://` 前缀
   会被 sed 拼成 `http://http//` 双协议）。
4. **PyPI 官方源（files.pythonhosted.org）国内直连必超时**：已把 `PIP_INDEX_URL` 默认设为
   清华 TUNA 镜像（`cd788267`），海外构建可 build-arg 覆盖回官方源。
5. **Docker Desktop「System proxy」模式会毒化 `docker build`**：把构建容器流量 transparent
   转发到 VM 内不可达的 `127.0.0.1:7897`，build-arg 无法绕过守护层拦截；`docker run` 走
   vpnkit 宿主侧转发所以通。**根治 = 关闭 Clash 系统代理再构建**，并显式传空 `HTTP_PROXY=`/`HTTPS_PROXY=`。
6. **ACR 拒收带 attestation 的 OCI 空清单**：见 4.2 末尾说明（`--provenance=false` / docreader 空重建）。
7. **GORM `.Select("*").Updates(struct)` 依然跳过零值字段**（bool false / 空字符串）：
   需要持久化零值时必须用 `Updates(map[string]interface{}{...})`，参考官方
   `internal/repository/datasource_repo.go` 的注释。
8. **`applyAuthSession` 用 `authSession.SystemAdmin` 字段写 ctx，不会从 user 自动复制**：
   每个构造 `authSession` 的地方都必须显式 `SystemAdmin: user.IsSystemAdmin`。
9. **platform key 有两条 dispatch 路径**：无 `X-Tenant-ID` 走 `attachPlatformAPIKeyAuthContext`；
   带 `X-Tenant-ID` 走 `attachAPIKeyAuthContext`。改鉴权/身份逻辑必须两条都改。
10. **Linux 容器内跑单测**：sqlite-vec cgo 需 `apt-get install libsqlite3-dev`；
    Windows 工作树 CRLF 检出会让 `TestDeploymentCapabilityKeysMatchFrontend` 假失败（`cat -A` 见 `^M$`），LF 化即可。
11. **`docker save -o` 路径**：Git Bash 下用 Windows 风格 `C:/workplace/...`，
    `/c/...` 会被 docker.exe 误解析。
12. **本机 Git Bash 的 native `ssh-keygen` 不认 `/c/...` POSIX 路径**（报
    "Saving key failed: No such file or directory"），必须用 Windows 风格
    `C:/Users/vante/.ssh/id_ed25519`。
