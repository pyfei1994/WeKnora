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
| 定制分支顶部 commit | `8ae36aac`（前端）、`9c59e317`（后端） |
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

---

## 3. 镜像（阿里云私有镜像库）

镜像库：`registry.cn-shanghai.aliyuncs.com/eftik/`

| 服务 | 镜像 | 当前 tag | Registry Digest |
| --- | --- | --- | --- |
| UI | `foxme-weknora-ui` | `0.7.2-foxme` / `latest` | `sha256:eb1e374c2312a5c56bb6ff6d730897f0af2d118a11767ffa4b5ab67162ccc994` |
| App | `foxme-weknora-app` | `0.7.2-foxme` / `latest` | `sha256:acd2cb35c47739036d3ba5c0d8a1e97e0a098b7903dfc592ea310b8218921b64` |
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
