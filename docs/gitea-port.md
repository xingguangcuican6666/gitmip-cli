# gh → Gitea 移植方案：丢弃 / 改造 / 新增

本文回答一个问题：把本仓库的 `gh` 移植到 Gitea API 上，哪些能力必须丢掉，哪些必须改写，哪些可以新增。

文中的端点与字段全部来自两份机器可读规范的逐条比对，以及 2026-11-05 对 `gitea.com` 与 `api.github.com` 的在线探测。凡标注「实测」的条目都经过真实请求验证；未标注的是从规范推导。证据来源见文末附录。

## 规范基准

| 项目 | Gitea | GitHub |
| --- | --- | --- |
| 规范 | Swagger 2.0，`/swagger.v1.json` | OpenAPI 3.0.3，`api.github.com.json` |
| 版本 | `29.0.0+dev-160-g55186ca39b` | `1.1.4` |
| API 根 | `/api/v1` | `/`（GHES 为 `/api/v3`） |
| 路径 / 操作 | 342 / 537 | 830 / 1259 |
| 数据模型 | 225 definitions | 1000 schemas |
| GraphQL | 无（规范中 0 次出现） | 有 v4 |

上述结论只对 `29.0.0+dev` 这个规范版本成立。较早的实例缺少 `/contents-ext`、`/actions/tasks`，`/repos/issues/search` 的限定参数也不全。落地前应先对目标实例抓一次 `/swagger.v1.json` 与 `GET /version`，重新比对本文引用的端点。

## 本仓库的现状

`pkg/cmd/` 下 39 个命令族，约 40 个顶层命令注册在 `pkg/cmd/root/root.go:135-179`。API 调用面：183 处 GraphQL 调用点，126 处 REST 调用点，79 个非测试文件引用 GraphQL。

这个比例决定了整体判断：**移植的主要工作量不是端点替换，而是把 GraphQL 那一半重写成 REST。** Gitea 没有任何 GraphQL，183 个调用点没有一条能原样保留。

---

## 一、架构决策先行

移植有两条路线，选哪条决定了下面所有条目的形态。

**路线 A：就地改写。** 保留现有包结构，逐个把 GraphQL 调用换成 Gitea REST 调用，把 `internal/ghinstance` 的分支改成 Gitea 分支。改动分散在数百个文件里，但每个命令的 CLI 行为、测试、帮助文本可以最大化保留。

**路线 B：适配层。** 新建 `internal/gitea`，向上提供一个 GitHub 形状的接口（拉取请求、议题、仓库的领域对象 + 分页 + 错误类型），命令层只依赖这个接口，不做主机构建。改动集中在少数几个包，命令层改动小，但需要一次性写出映射层，且映射层本身会成为长期负债。

建议走 **A 为主、局部 B**：`ghinstance`、`api/client.go`、`api/query_builder.go`、`pkg/search/query.go` 这几处做成可选后端（编译期或主机类型开关），其余命令按下面的清单逐族改写。理由是这些命令的 REST 版本在 Gitea 上形态差异太大，适配层为了抹平差异会引入大量近似语义，反而比直写更难维护。

下面的清单按这条路线的粒度给出。

---

## 二、要丢掉的

### 2.1 整族删除（Gitea 完全无对应 API）

| 命令 | 依据 |
| --- | --- |
| `gh codespace`（含 `internal/codespaces`、dev tunnel） | Gitea 无 `/codespaces` 任何路径；GitHub 侧 48 个操作 |
| `gh gist` | Gitea 无 `/gists`。规范全表中不存在该前缀 |
| `gh discussion` | Gitea 无 `/discussions`。`gh` 侧完全基于 GraphQL |
| `gh ruleset` | 无 `/rulesets`、无 `/rules/branches/{branch}`。Gitea 的分支保护是另一套模型，替代方案见第四节 |
| `gh cache` | 无 `/actions/caches`。Gitea 不暴露缓存管理 |
| `gh attestation` | 无 attestations / sigstore 相关端点 |
| `gh agent-task` | 无 `/agents/*` |
| `gh copilot` | 无 copilot 相关端点；现有实现还依赖 `github.com/github/copilot-cli` 下载源 |
| `gh skills` | 依赖 `/search/code` 与 GitHub 专有的 skills 分发 |

以上 9 族建议直接从 `root.go` 摘除，同时移除对应 `pkg/cmd/<family>` 目录与 `pkg/cmd/factory` 中仅供其使用的装配。

### 2.2 子命令删除（族保留，部分子命令无后端）

| 子命令 | 原因 | 建议 |
| --- | --- | --- |
| `gh search code` | 无全局 `/search`，亦无代码搜索端点 | 删除 |
| `gh search commits` | 无提交搜索端点 | 删除 |
| `gh repo autolink` | 无 `/autolinks` | 删除 |
| `gh repo credits` | 依赖 `repos/{o}/{r}/contributors`，Gitea 规范中不存在（实测确认） | 删除，或改为基于 `/commits` 本地聚合（语义不等价，不建议） |
| `gh issue transfer` | 无 issue 跨仓库转移端点 | 删除 |
| `gh issue develop` | linked branch 是 GitHub 专有模型，Gitea 无对应端点 | 删除 |
| `gh pr revert` | 依赖 GraphQL `revertPullRequest`，Gitea 无 revert API | 删除 |
| `gh pr ready` | 需要把草稿转正式；`CreatePullRequestOption` 与 `EditPullRequestOption` 都没有 `draft` 字段 | 删除。Gitea 只能读 `PullRequest.draft`，无法通过 API 写入 |
| `gh ssh-key` 的 signing key 路径 | 代码走 `user/ssh_signing_keys`（`pkg/cmd/ssh-key/shared/user_keys.go:51`），Gitea 无该端点 | 改用 `/user/keys`，删除签名密钥相关分支 |
| `gh api graphql` | 无 GraphQL 端点 | 删除该子路径与 `pkg/cmd/api/http.go:17` 的特殊分支 |
| `gh auth refresh` | 依赖 GitHub OAuth refresh token 轮换 | 删除或改为重新登录 |

### 2.3 机制性删除

| 机制 | 位置 | 说明 |
| --- | --- | --- |
| Node ID | `api/node_id.go` | 客户端用 base64 MessagePack 合成 `PR_`/`U_`/`DC_` 前缀的 node ID。Gitea 对象无 `node_id` 字段，整个机制无意义 |
| 能力探测 | `internal/featuredetection/feature_detection.go` | 全部建立在 GraphQL schema introspection 上。改用 `GET /version` + 版本比较 |
| 速率限制展示 | 分散在 `pkg/cmd/skills/search`、`internal/attachments` | Gitea 实测不返回 `x-ratelimit-*`，且无 `/rate_limit`。删掉相关分支 |
| 条件请求缓存 | 无 | `gh` 本就不用 ETag，Gitea 也不返回。无需处理 |
| 遥测上报 | `pkg/cmd/send-telemetry/send_telemetry.go:21` | 硬编码 `cafe.github.com`。私有部署应删除该命令 |
| 自动更新 | `internal/update/update.go:121` | 硬编码 `api.github.com/repos/.../releases/latest`。改为自有发布源或删除 |
| 无障碍合规上报 | `pkg/cmd/accessibility/accessibility.go:15` | 硬编码 `accessibility.github.com`。删除 |
| OAuth device flow | `internal/authflow/flow.go:23` | 硬编码 GitHub 的 client ID `178c6fc778ccc68e1d6a`。Gitea 的 OAuth2 应用模型不同（`/user/applications/oauth2`），要么自建 OAuth2 应用，要么退化为 PAT-only |

---

## 三、要改动的

按改动所在的层次排列，下层改动影响面最大。

### 3.1 传输层

| 项目 | 现状 | 必改 |
| --- | --- | --- |
| 版本头 | `api/client.go:19` 对每个请求写死 `X-GitHub-Api-Version: 2022-11-28` | Gitea 忽略该头，不致命，但应在 Gitea 主机上摘除 |
| 认证头 | `Authorization: token <t>` | 与 Gitea 的 `AuthorizationHeaderToken` 格式一致，**无需改动** |
| 错误体解析 | GitHub 形状 `{message, documentation_url, status}` | Gitea 为 `{message, url}`，且 `message` 小写（实测 `"not found"`）。凡依据错误文本分支的逻辑都要复核 |
| 媒体类型 | `application/vnd.github.*` 系列 | 全部失效，逐一替换，见 3.2 |
| CORS | `access-control-allow-origin: *`，`Access-Control-Expose-Headers` 白名单（实测） | 实测：仅当请求带 `Origin` 头时才回 `access-control-allow-origin: *`；`Access-Control-Expose-Headers` 只出现在分页端点（`Link, X-Total-Count` 等），非分页端点没有 |
| 分页 | `Link` 头 `rel="next"`（`pkg/cmd/api/pagination.go:15`） | Gitea 实测也发 `Link`，**解析代码可复用**。但参数名与上限要改，见 3.3 |

### 3.2 媒体类型替换表

| 用途 | gh 现状 | Gitea 等价 |
| --- | --- | --- |
| PR diff | `Accept: application/vnd.github.v3.diff`（`pkg/cmd/pr/diff/diff.go:213-225`） | **URL 后缀** `GET .../pulls/{index}.diff`。实测 Gitea 忽略该 Accept 头并返回 JSON（10521 字节），必须改机制 |
| PR patch | `Accept: application/vnd.github.v3.patch` | `GET .../pulls/{index}.patch`，另支持 `?binary=true` |
| 原始文件 | `Accept: application/vnd.github.raw`（`pkg/cmd/repo/read-file/http.go:96,178`） | `GET /repos/{o}/{r}/raw/{filepath}`（实测 200，`text/plain`） |
| 目录/对象 | `Accept: application/vnd.github.object+json` | `GET /repos/{o}/{r}/contents-ext/{filepath}?includes=` 或普通 `/contents` |
| commit sha | `application/vnd.github.v3.sha`（`pkg/cmd/extension/http.go:180`） | 无对应，读 JSON 的 `sha` 字段 |
| topics 预览 | `application/vnd.github.mercy-preview+json`（`pkg/cmd/repo/edit/edit.go:579,620`） | 无关，Gitea 的 `/topics` 是普通端点 |
| 搜索高亮 | `application/vnd.github.text-match+json`（`pkg/search/searcher.go:261`） | 无代码搜索，整块删除 |
| 发布资产上传 | `POST` 到响应里的 `upload_url` | `POST /repos/{o}/{r}/releases/{id}/assets?name=<n>`，multipart 或 `application/octet-stream` |

### 3.3 分页与查询参数

| 项目 | GitHub | Gitea |
| --- | --- | --- |
| 每页大小 | `per_page`（规范中 253 处） | `limit`（112 处） |
| 页号 | `page` | `page`，同名 |
| 上限 | 100 | 实例配置 `MAX_RESPONSE_ITEMS`。**实测 gitea.com 上 `limit=100` 只返回 50** |
| 总数 | 无 | `x-total` 与 `x-total-count`（实测 638） |
| 游标 | `after`/`before`（32/31 处） | 无 |

需要改写的地方：`pkg/cmd/api/pagination.go` 里所有 `per_page` 相关逻辑（`api_test.go` 中多处断言 `per_page=50`），`gh pr list`/`gh issue list` 的批量抓取逻辑，以及任何假设"一次最多 100 条"的循环终止条件。上限 50 会让现有循环多跑一倍轮次，但不改变正确性。

`x-total-count` 是 Gitea 多出来的能力，`gh <cmd> list --limit` 的截断提示可以改成精确值。

### 3.4 主机与端点解析

`internal/ghinstance/host.go` 是移植的第一个必改点：

| 函数 | 现状 | Gitea 需要 |
| --- | --- | --- |
| `RESTPrefix`（`:59`） | 非 dotcom / 非 tenancy / 非 localhost 一律返回 `https://{host}/api/v3/` | 返回 `https://{host}/api/v1/` |
| `GraphQLEndpoint`（`:46`） | 返回 `https://{host}/api/graphql` | 删除。Gitea 无 GraphQL |
| `GistPrefix`/`GistHost`（`:72`,`:80`） | `gist.{host}` 子域 | 删除 |
| `UserAssetUploadPrefix`（`:95`） | `uploads.{host}` | 删除，Gitea 附件走仓库内 `/issues/{index}/assets` |
| `CategorizeHost`（`:109`） | `github.com` / `ghes` / `tenancy` | 需要新增 `gitea` 类别 |

`pkg/cmd/api/http.go:17-35` 重复实现了同一套判定，要同步改。

另外 `pkg/cmd/factory/default.go` 的仓库解析（`BaseRepoFunc:83`、`SmartBaseRepoFunc:161`）假定远端走 GitHub 的 host 映射，Gitea 实例通常没有 `api.` 子域，需要按主机名直接映射到同一域名的 `/api/v1`。

### 3.5 GraphQL → REST 重写清单

这是最大的一块。逐族给出替换目标：

**`gh pr`**

| 子命令 | 现实现 | 替换为 |
| --- | --- | --- |
| `view` / `list` | `pkg/cmd/pr/shared/finder.go:356,386`、`lister.go:52-118` | `GET /repos/{o}/{r}/pulls/{index}`、`GET .../pulls` |
| `status` | `pkg/cmd/pr/status/http.go:200-222` | `GET /repos/issues/search?assigned=true&review_requested=true&created_by=<me>` |
| `create` | `api/queries_pr.go:490,539,586` | `POST .../pulls`。注意：无 `draft` 字段，`labels` 需传 **label ID 数组** |
| `merge` | `pkg/cmd/pr/merge/http.go`（全 GraphQL） | `POST .../pulls/{index}/merge`，方法由 PUT 改为 **POST**，字段 `merge_method` → `do`（必填） |
| `checks` | `pkg/cmd/pr/checks/checks.go:257-281`，依赖 `statusCheckRollup.contexts` 的 CheckRun/StatusContext 联合 | `GET .../commits/{ref}/status`（`CombinedStatus`）。**能力降级**：无 check run 详情、无 annotations、无必需检查判定 |
| `diff` | Accept 头 | `.diff` 后缀，见 3.2 |
| `update-branch` | `PUT .../pulls/{n}/update-branch`，body `{branch}` | `POST .../pulls/{index}/update?style=merge\|rebase`，**参数在查询串里，无 body** |
| `review` | `POST .../reviews`，event `APPROVE` | 同端点，event 改为 `APPROVED`（见 3.6） |
| `checkout` | git 层 | `head.ref` 不可直接当分支名，见 3.6 |
| `comment` / `close` / `reopen` / `edit` | GraphQL mutation | `POST .../issues/{index}/comments`、`PATCH .../pulls/{index}` |

**`gh issue`**

| 子命令 | 替换为 |
| --- | --- |
| `view` / `list` | `GET /repos/{o}/{r}/issues/{index}`、`GET .../issues` |
| `create` | `POST .../issues`。`labels` 为 ID 数组，`milestone` 为 int64 |
| `edit` | `PATCH .../issues/{index}` |
| `comment` | 写入 `POST .../issues/{index}/comments`；读取、编辑、删除用 `.../issues/comments/{id}`（该路径有 GET，而 `.../issues/{index}/comments/{id}` 只有 PATCH/DELETE） |
| `delete` | GraphQL `deleteIssue`（`pkg/cmd/issue/delete/delete.go:119-136`）→ `DELETE .../issues/{index}`。Gitea 用序号而非 node ID，`apiDelete` 的签名与前置查询都可简化 |
| `lock`/`unlock` | `PUT`/`DELETE .../issues/{index}/lock` |
| `pin`/`unpin` | `POST`/`DELETE .../issues/{index}/pin` |
| `artifact` | Gitea 是附件而非工件：`.../issues/{index}/assets`，语义要重写 |
| `status` | `/repos/issues/search`，限定参数比 GitHub 少，见 3.7 |

**`gh repo`**

| 子命令 | 替换为 |
| --- | --- |
| `view` | `GET /repos/{o}/{r}`。`stars_count` 而非 `stargazers_count`；`license` 在 Gitea 是 `licenses` 数组；无 `node_id` |
| `list` | `GET /user/repos` + `GET /repos/search` 组合。Gitea 的 `/repos/search` 支持 `q`/`topic`/`sort`/`order`/`archived`/`template`，但没有 GitHub 的语言/可见性枚举 |
| `create` | `POST /user/repos` 或 `POST /orgs/{org}/repos`。**`CreateRepoOption` 没有 website/homepage 字段**，需要二次 `PATCH` |
| `edit` | `PATCH /repos/{o}/{r}`。homepage 对应 Gitea 的 `website` |
| `sync` | `POST /repos/{o}/{r}/merge-upstream`，同名端点 |
| `fork` | `POST /repos/{o}/{r}/forks` |
| `rename`/`setdefault`/`archive`/`delete` | `PATCH`/`PATCH`/`PATCH archived`/`DELETE` |
| `deploy-key` | `GET`/`POST /repos/{o}/{r}/keys` |
| `read-file`/`read-dir` | `/raw/{filepath}` 或 `/contents` |

**`gh release`**：`latest`、`tags/{tag}`、`{id}` 均同名。差异在于资产路径从扁平的 `/releases/assets/{asset_id}` 变为嵌套的 `/releases/{id}/assets/{attachment_id}`，上传改为 multipart 直传。`generate-notes` 与 `make_latest` 无对应，相关子逻辑删除。

**`gh label`**：`list`/`create`/`delete` 直接可用，但 Gitea 的路径是 `/labels/{id}` 而非 `/labels/{name}`。`edit`、`clone`、`delete --yes` 都需要先 `GET /repos/{o}/{r}/labels` 建立名称→ID 映射。颜色格式也要转换，见 3.6。

**`gh org`**：`list` 用 `GET /user/orgs`。Gitea 还支持 `POST /orgs` 建组织，GitHub REST 没有，可据此新增。

**`gh search`**：`issues` → `/repos/issues/search`；`repos` → `/repos/search`；`prs` → `/repos/issues/search?type=pulls`；`users` → `/users/search`（Gitea 有，`gh` 无对应子命令）。`pkg/search/query.go` 的 377 行限定符构造器整体作废，见 3.7。

**`gh status`**：`pkg/cmd/status/status.go:401-443` 的 GraphQL `search(type: ISSUE)` 改为 `GET /repos/issues/search`，用 `assigned`、`review_requested`、`mentioned`、`created_by` 四个布尔/字符串参数组合出三段视图。

**其余 REST 族**（`gh run`、`gh workflow`、`gh secret`、`gh variable`、`gh api`、`gh ssh-key`、`gh gpg-key`、`gh browse`、`gh auth`）改动较小，见 3.8。

### 3.6 字段与枚举映射

这些是最容易漏、且不会在编译期报错的差异：

| 概念 | GitHub | Gitea | 处理 |
| --- | --- | --- | --- |
| star 数 | `stargazers_count` | `stars_count` | 改名 |
| 锁定标记 | `locked` | `is_locked` | 改名 |
| 主页 | `homepage` | `website` | 改名 |
| PR/issue 序号路径参数 | `{pull_number}` / `{issue_number}` | `{index}` | 路径模板改写 |
| run ID 路径参数 | `{run_id}` | `{run}` | 路径模板改写 |
| 文件路径参数 | `{path}` | `{filepath}` | 路径模板改写 |
| 附件/工件 | `artifacts` | `assets` | 改名 |
| 标签标识 | 名称字符串 | **ID (int64)** 或名称，二者皆可 | 写入需先解析 ID，读取需反查名称 |
| 标签颜色 | `00aabb` | `00aabb` | **实测无需转换**。规范示例写 `#00aabb`，但线上返回的是 `fbca04`，与 GitHub 同格式；`gh` 本来就去掉 `#` 再提交 |
| 评审 event | `APPROVE` | `APPROVED` | 枚举映射 |
| 合并方式 | `merge_method`（可选，3 值） | `do`（**必填**，6 值） | `merge`/`squash`/`rebase` 直接对应；Gitea 的 `rebase-merge`、`fast-forward-only`、`manually-merged` 无 GitHub 等价，反向映射时只能取默认 |
| 合并消息 | `commit_title`/`commit_message` | `merge_title_field`/`merge_message_field` | 改名 |
| 合并并发控制 | `sha` | `head_commit_id`/`merge_commit_id` | 改名 |
| 状态枚举 | `error`/`failure`/`pending`/`success` | 另加 `warning`/`skipped` | 反向映射需处理 Gitea 独有的两个值 |
| 团队寻址 | `{team_slug}` 或 `{team_id}` | **仅数字 `{id}`** | 无法按 slug 寻址，需先查 ID |
| Issue 关闭原因 | `state_reason` | 无 | 该信息不可表达，相关 UI 删除 |
| 乐观锁 | 无 | `content_version` | Gitea 独有，可用作并发保护 |
| PR 的 head 分支名 | `head.ref` 是分支名 | `head.ref` **取决于 PR 状态**，见下 | 必须改用 `head.label` |

最后一条需要展开，因为它是实测发现的、文档里没有的陷阱。对同一个仓库 `xorm/xorm` 取样：开放中的 PR（#2621、#2620）`head.ref` 是正常分支名 `fix/mssql-insert-multi-chunk`；已关闭或已合并的 PR（#2622、#2616、#2614、#2613）`head.ref` 全部变成 `refs/pull/<N>/head`，而 `head.label` 在所有情况下都保持分支名。`gh pr checkout` 依赖这一点：`pkg/cmd/pr/checkout/checkout.go:203` 用 `+refs/heads/%s` 拼 refspec，`:201` 用 `remote/branch` 拼远端分支，两者都假定 `HeadRefName` 是裸分支名。移植时读取 `head.label` 并去掉跨仓前缀（`owner:branch`），不要读 `head.ref`。

另需注意 `head.repo` 可能为 `null`（源仓库已删除），此时 `head.repo_id` 仍存在，而 Gitea 的 `head` 对象不含 `user` 字段（GitHub 有）。

### 3.7 搜索能力降级

`pkg/search/query.go` 构造的是 GitHub 查询语法：`repo:`、`is:`、`label:`、`review:`、`team-review-requested:`、`in:`、`advanced_search=true`、`search_type=semantic|hybrid`。Gitea 没有查询语言，只有各端点上的枚举/布尔参数：

`/repos/issues/search` 支持的参数实测为 `state`、`labels`、`milestones`、`q`、`type`、`since`、`before`、`assigned`、`created`、`mentioned`、`review_requested`、`reviewed`、`owner`、`created_by`、`team`、`page`、`limit`。

`/repos/search` 支持 `q`、`topic`、`includeDesc`、`uid`、`priority_owner_id`、`team_id`、`starredBy`、`private`、`is_private`、`template`、`archived`、`mode`、`exclusive`、`sort`、`order`、`page`、`limit`。

差异后果：`gh search issues` 的 `--author`、`--assignee`、`--involves`、`--mentions`、`--state`、`--label` 基本可映射；`created:`/`updated:` 日期范围、`comments:` 数量限定、`reactions:`、多 `label:` 与/或语义无法表达。需要把这些 flag 标记为不支持并返回明确错误，而不是静默忽略。

响应体形状也不同：Gitea 的 `/repos/search` 与 `/users/search` 返回 `{"ok": bool, "data": [...]}`，而 `/repos/issues/search` 返回裸数组（`responses.IssueList`）。逐端点确认。

### 3.8 认证与密钥

| 项目 | 现状 | 必改 |
| --- | --- | --- |
| scope 展示 | `pkg/cmd/auth/shared/oauth_scopes.go:35` 读 `X-Oauth-Scopes` 响应头 | 实测 Gitea 不发该头。改用 `GET /token`，响应含 `scopes` 字段（`CurrentAccessToken` schema：`created_at`/`id`/`last_used_at`/`name`/`scopes`/`user`） |
| 最小 scope 校验 | `HasMinimumScopes` | Gitea 的 scope 模型不同，需按实例实际 scope 重定义 |
| 令牌管理 | 无 | Gitea 有 `GET`/`DELETE /token`、`GET`/`POST /users/{username}/tokens`、`DELETE /users/{username}/tokens/{token}`。可新增 `gh auth token` 的管理能力 |
| 管理员代操作 | 无 | Gitea 的 `Sudo: <user>` 头或 `sudo` 查询参数。整理为 `gh api --sudo` 之类 |
| 2FA | 不需要 | Gitea 的 `X-GITEA-OTP` 仅在 Basic Auth 场景需要，token 认证不需要 |
| 密钥环 | `gh:<hostname>` 服务名 | 可保留，主机名天然隔离 |

### 3.9 Secrets / Variables

| 项目 | 现状 | 必改 |
| --- | --- | --- |
| 应用维度 | `pkg/cmd/secret/shared/shared.go:21-24` 的 `{actions\|codespaces\|dependabot}` | 收窄为 `actions` 单维度 |
| 加密 | GitHub 需要 `GET .../secrets/public-key` + libsodium sealed box | Gitea 的 `CreateOrUpdateSecretOption` 只要 `{data, description}`，服务端自己加密。**去掉整个加密路径**，实测 Gitea 无 `public-key` 端点 |
| 作用域 | repo / org / user / environment / codespaces / dependabot | repo / org / user（三级），无 environment |
| 用户级列表 | `GET /user/actions/secrets` | **不存在**。Gitea 只有按名 `PUT`/`DELETE /user/actions/secrets/{secretname}`，无法列出 |
| Variables | 同上多维度 | repo / org / user 三级，端点齐全，`UpdateVariableOption` 支持改名 |

### 3.10 Actions

| 项目 | GitHub | Gitea |
| --- | --- | --- |
| Run 路径参数 | `{run_id}` | `{run}` |
| Run 日志 | `GET .../runs/{run_id}/logs` | `GET .../runs/{run}/logs`（同名同义） |
| 工件下载 | `GET .../artifacts/{id}/zip` | 同名 |
| Workflow dispatch | body `{ref, inputs}`，另有 `return_run_details` | `CreateActionWorkflowDispatch{ref*, inputs}`，无 `return_run_details` |
| Enable/Disable | `PUT .../workflows/{id}/enable` | 同名同义 |
| Attempts 重跑 | `POST .../attempts/{n}/rerun` | 无该端点（Gitea 有 `.../attempts/{attempt}` 只读） |
| 缓存 | `/actions/caches` 全套 | 无 |
| Environment | `/environments` 12 个操作 | 无 |
| Runner | repo/org/user/admin 四级 | 同名四级，另有 `actions/tasks` |

`gh run watch` 需要日志流式读取，Gitea 返回的是整包 zip 或纯文本，轮询行为要重新设计。

---

## 四、要新增的

Gitea 有而 `gh` 没有的能力，移植时顺手补上：

| 新增命令（建议） | 对应端点 | 价值 |
| --- | --- | --- |
| `gh repo wiki`（list/view/create/edit/delete） | `/repos/{o}/{r}/wiki/pages`、`/wiki/page/{pageName}`、`/wiki/new`、`/wiki/revisions/{pageName}` | Gitea 原生 wiki，GitHub REST 没有 |
| `gh issue time`（list/add/delete） | `/repos/{o}/{r}/issues/{index}/times`、`/repos/{o}/{r}/times`、`/user/times` | 时间跟踪，另有 `/issues/{index}/stopwatch/start`、`/stop`、`/delete` |
| `gh repo mirror`（sync/list） | `/repos/{o}/{r}/mirror-sync`、`/repos/{o}/{r}/push_mirrors` | 拉取镜像与推送镜像 |
| `gh repo transfer`（accept/reject） | `/repos/{o}/{r}/transfer`、`/transfer/accept`、`/transfer/reject` | Gitea 是三步流程，比 GitHub 的单步更完整 |
| `gh org create` / `gh org rename` | `POST /orgs`、`POST /orgs/{org}/rename` | GitHub REST 不支持建组织 |
| `gh org remove-repo` | `DELETE /orgs/{org}/repos/{repo}` | GitHub 无对应 |
| `gh auth token list/delete` | `GET`/`DELETE /token`、`/users/{username}/tokens` | 令牌自助管理 |
| `gh branch-protection`（list/create/edit/delete/priority） | `/repos/{o}/{r}/branch_protections`、`/priority` | 替代 `gh ruleset`，模型不同但能覆盖需求 |
| `gh repo topic`（已有 `gh repo edit --add-topic`） | `/repos/{o}/{r}/topics`、`/topics/{topic}` | Gitea 支持单个 topic 增删 |
| `gh markup` / `gh markdown` | `POST /markup`、`POST /markdown`、`/markdown/raw` | 本地渲染预览 |
| `gh settings` | `/settings/api`、`/settings/attachment`、`/settings/repository`、`/settings/ui` | 实例配置查看 |
| `gh signing-key` | `/signing-key.gpg`、`/signing-key.pub`、`/repos/{o}/{r}/signing-key.gpg` | 验证提交签名 |
| `gh api --sudo <user>` | `Sudo` 头 / `sudo` 参数 | 管理员代操作 |
| `gh tag create` | `POST /repos/{o}/{r}/tags`（`CreateTagOption{tag_name*, message, target}`） | GitHub 只能走 `git/tags` |
| `gh repo create --object-format sha256 --trust-model ...` | `CreateRepoOption.object_format_name`、`trust_model` | SHA-256 仓库 |
| `gh repo units` | `EditRepoOption` 的 `has_actions`/`has_code`/`has_packages`/`has_releases`/`has_wiki`/`has_issues`/`has_projects`/`has_pull_requests` | 按 unit 开关子功能 |
| `gh repo file move` | `UpdateFileOptions.from_path` | 通过 contents API 移动/重命名文件 |
| `gh admin`（用户 CRUD、cron、unadopted） | `/admin/*` 共 33 个操作 | 实例运维 |
| `gh pr list --pinned` | `GET /repos/{o}/{r}/pulls/pinned` | Gitea 独有 |
| 精确总数 | `x-total-count` 响应头 | 让 `--limit` 的截断提示显示真实总数 |

另有 `GET /pulls/{base}/{head}` 可以直接按分支对查 PR，比 GitHub 用 `head=owner:branch` 过滤列表更准确，可用作 `gh pr view <branch>` 的实现基础。

---

## 五、落地顺序

按依赖关系排，每一步都能独立验证。第 1-7 步已落地（`issue`/`pr` 主线在第 5 步），第 8-9 步未开始。

1. ✅ **传输与主机层**。新增 `internal/hosttype`，以 hosts.yml 里的 `host_type: gitea` 声明主机类别（默认按主机名形状推断，github.com/ghe.com 恒为 GitHub；无环境变量开关，与 `GH_HOST` 的既有约定一致——主机类别是配置的一部分，不是每次调用的临时状态）。`RESTPrefix` 返回 `/api/v1`，`GraphQLEndpoint` 对 Gitea 主机报错，`pkg/cmd/api/http.go` 不再拼 `/api/v3` 前缀。已实测 `gh api --hostname gitea.com version` 返回 `{"version":"29.0.0+dev-160-g55186ca39b"}`。
2. ✅ **分页与错误**。`pagination.go` 在 Gitea 主机上用 `limit`/`page`，上限取 50（实测 `limit=100` 只返回 50 条），错误体按 `{"message","url"}` 解析。已实测翻页。
3. ✅ **认证**。`gh auth login --with-token` 走通；`HasMinimumScopes` 对 Gitea 主机改读 `GET /token` 的 `scopes` 数组，并按 `write:*` 蕴含 `read:*`、`all` 蕴含一切的规则校验；`gh auth status` 显示实际 scope 名。device flow 未接（需自建 OAuth2 应用）。
4. ✅ **纯 REST 命令族**，均带单元测试并对 `gitea.com` 实测：
   - `repo view`：`GET /repos/{o}/{r}` 映射到 `api.Repository`；README 走 `GET /contents` 列目录再取文件（Gitea 无 `/readme` 端点）
   - `repo list`：`GET /users/{username}` 解析 owner 数字 id，再 `GET /repos/search?uid=&exclusive=true`；语言过滤在本地逐页做，主题过滤仅支持单个
   - `label`：`GET/POST/PATCH/DELETE /labels`。Gitea 按数字 id 寻址标签，改名与删除前需先列目录解析 id；标签名无唯一约束，重名时报歧义错误而非任选其一
   - `release list`：`GET /releases`。`draft`/`pre-release` 是**包含**过滤（`draft=true` 只返回草稿），`isLatest` 由本地取最新非草稿非预发布，`--order asc` 本地反转
   - `release view`：`GET /releases/tags/{tag}`，未命中再 `GET /releases?draft=true` 本地匹配待发布标签（Gitea 无 GraphQL，GitHub 靠它查草稿）
   - `org list`：复用 `api.CurrentLoginNameAndOrgs`（`GET /user` + `GET /user/orgs`）
   - `browse`：`LastCommit` 改为两次 REST（取默认分支 + `GET /commits?limit=1&sha=`）
   - `variable`：list/get/set/delete 全部改 REST。响应是裸数组，值字段叫 `data`；名字在路径里，创建 POST、更新 PUT，409 触发更新；无时间戳与 visibility
   - `secret`：`PUT /actions/secrets/{name}` 明文提交，无 public-key 端点，故无加密、`--no-store` 明确报错；无 Codespaces/Dependabot/Agents/environment 存储，`--app` 只接受 `actions`
   - 另加 `internal/featuredetection` 的 Gitea 检测器：所有能力位按实际情况返回常量，不再发 GraphQL 内省请求
5. 🟡 **`pr` 与 `issue` 的 GraphQL 重写**。工作量最大，按子命令分批，每批保留原有单元测试作为行为契约（`httpmock` 层从 GraphQL 断言改为 REST 断言）。
   - ✅ `issue` 全族：list / view / create / close / reopen / delete / edit / lock / unlock / pin / unpin / comment / status。`transfer` 与 `develop` 明确拒绝（Gitea 无端点）。
   - ✅ `pr view` / `pr list` / `pr diff` / `pr comment` / `pr merge` / `pr checkout`。
   - ✅ `pr checks`：`GET /commits/{sha}/status` 一次取回全部 status。Gitea 的 `pending/success/error/failure/warning/skipped` 映射到三桶时，`warning` 与 `skipped` 归入 skipping（都不阻塞合并），否则 `--watch` 会永远等下去。`--required` 拒绝：Gitea 不按 commit status 暴露必检项。
   - ✅ `pr status`：两个桶走 Gitea 自己的端点。`created_by=<login>` 用仓库内 issue 列表（精确）；`review_requested=true` 只有跨仓库搜索支持，且必须认证。跨仓库搜索的 `created` 在匿名时会被**静默忽略**并返回 owner 下全部 PR，所以不能用它做 created 桶。当前分支的 PR 本地匹配 head 后在取一次详情并预加载 rollup。
   - ✅ `pr review`：`POST /pulls/{index}/reviews`，`event` 取 `APPROVED`/`COMMENT`/`REQUEST_CHANGES`；正文由 `body` 承载，空值省略。
   - ✅ `pr edit`：`PATCH /issues/{index}` 改标题/正文/里程碑，标签走 `PUT /issues/{index}/labels`（整表替换），assignee 走 `POST|DELETE /issues/{index}/assignees`，基线单独 `PATCH /pulls/{index}`，reviewer 走 `POST|DELETE /pulls/{index}/requested_reviewers`。
   - ✅ `pr ready`：拒绝。`EditPullRequestOption` 无 `draft` 字段，`POST /pulls/{index}/update` 是"合并基线到 head"不是"转正"，Gitea 的 API 层没有清 draft 位的手段。
   - ✅ `pr close` / `pr reopen`：`PATCH /pulls/{index}` 带 `state: closed|open`，按 index 寻址。
   - ✅ `pr update-branch`：`POST /pulls/{index}/update`（Gitea 语义是 merge 基线进 head），`--rebase` 拒绝。behind 计数不能靠反向 compare：`owner:branch...base` 在 Gitea 上 404，改用 PR 自己的 `merge_base` 与 base 比较。
   - ✅ `pr create`：`POST /pulls` + `CreatePullRequestOption`（`base`/`head`/`title`/`body`/`assignees`/`labels`/`milestone`/`reviewers`）。`--draft` 与 `--project` 拒绝（无字段）。
   - ✅ `pr revert`：拒绝。Gitea 无 revert 端点。
6. ⬜ **`search` 与 `status`**。限定符降级，明确报错而非静默忽略。`gh search` 可接 `/repos/search`、`/repos/issues/search`、`/users/search`；issue search 支持 `created`/`assigned`/`mentioned`/`review_requested`/`reviewed` 布尔参数，但没有 GitHub 的 `key:value` 语法。
7. ✅ **`secret`、`actions` 族**的 secret 部分（见第 4 步）。`actions` 子命令族（run/job/workflow）未开始。
8. ⬜ **删除清单**。最后统一摘除 2.1/2.2 的命令与 `root.go` 注册。
9. ⬜ **新增清单**。按实际需求挑选，不影响主线。

### 实现约定

落地时反复用到两条，后续步骤应沿用：

- **主机分派集中在一个入口**。每个被改写的函数在开头判 `hosttype.IsGitea(host)` 后转入同包的 `*_gitea.go`，GitHub 分支一行未动。这样回归风险局限在新文件里，删掉新文件即可回退。
- **写入用 `Request` 而非 `REST`**。Gitea 的创建成功回 201 且**无响应体**（标签 POST、变量 POST、secret PUT 都是），而 `api.Client.REST` 会把所有 2xx 当 JSON 解码，空体会报 `unexpected end of JSON input`。需要读响应头的（`X-Total-Count`）同样只能走 `Request`。

`X-GitHub-Api-Version` 仍会发往 Gitea 主机。go-gh 在自己的 header tripper 里注入该头，gh 侧的 `SkipDefaultHeaders` 与自定 headers 都盖不掉它；Gitea 忽略未知头，实测无影响，故未做逐请求剥离。

---

## 六、验证点

移植完成后应逐条实测，这些是编译期不会报错但会静默出错的地方：

- `limit` 上限：对目标实例发 `limit=100`，确认实际返回条数（gitea.com 为 50），据此验证分页循环不会漏数据
- `.diff` / `.patch` 后缀：确认返回 `text/plain` 而非 JSON
- 标签往返：写 ID、读名称，确认 `edit`/`clone` 后名称未错配
- 合并：`do` 必填，确认 `merge`/`squash`/`rebase` 三种都能成功，且 `rebase-merge` 之类的 Gitea 独有值不会被反向映射误用
- 评审：`APPROVED` 而非 `APPROVE`
- 错误分支：404 时 `message` 为 `"not found"`（小写），确认依赖文本的判断未被破坏
- 仓库创建：确认 `website` 需要二次 PATCH，`CreateRepoOption` 不接受
- Secrets：确认 `data` 明文提交后能在 Actions 中使用
- 团队：确认只能按数字 ID 寻址
- 分页头：确认 `Link` + `x-total-count` 同时存在（实测有）

---

## 七、风险

**Gitea 版本漂移。** 本文基于 `29.0.0+dev`。1.22 及更早没有 `/contents-ext`、`/actions/tasks`、`/repos/issues/search` 的部分限定参数。落地前应先对目标实例跑一次 `GET /version` + 规范抓取，重新比对。

**实例配置差异。** `MAX_RESPONSE_ITEMS`、`DEFAULT_PAGING_NUM`、是否开放 `/admin`、是否启用 Actions，都是实例级配置。同一份代码在不同实例上的行为可能不一致。

**能力降级不可逆。** `gh pr checks`、`gh search issues` 的限定符、environment 级 secrets、check-run annotations 这几处，Gitea 侧没有等价数据源。降级后的输出必须显式标注缺失，不能让用户以为是完整结果。

**上游同步成本。** 本仓库持续跟随 `cli/cli` 上游。改了 `internal/ghinstance`、`api/client.go`、`pkg/cmd/api/*` 这些高频变更点后，每次上游合并都会冲突。建议把 Gitea 分支集中在少数几个有清晰边界的文件里，其余改动尽量放在新包里，减少与上游的直接交叠。

---

## 附录：证据

规范来源：

- Gitea：`https://gitea.com/swagger.v1.json`（Swagger 2.0，342 paths / 537 operations / 225 definitions / 149 responses）
- GitHub：`rest-api-description` 仓库的 `descriptions/api.github.com/api.github.com.json`（OpenAPI 3.0.3，830 paths / 1259 operations / 1000 schemas）

在线探测（2026-11-05）确认的事实：

- Gitea `GET /version` → `{"version":"29.0.0+dev-160-g55186ca39b"}`
- Gitea `/repos/search` 响应头含 `link`、`x-total-count: 932`；`/repos/{o}/{r}/commits` 含 `x-total: 638`
- Gitea `limit=100` / `limit=200` 在 gitea.com 上均只返回 50 条
- Gitea 无 `etag`、无 `last-modified`、无 `x-ratelimit-*`
- Gitea 的 `access-control-allow-origin: *` 仅在请求带 `Origin` 头时返回；`Access-Control-Expose-Headers` 只在分页端点出现（`repos/search` 为 `Link, X-Total-Count`，`commits` 另含 `X-Page/X-PerPage/X-Total/X-PageCount/X-HasMore`）
- Gitea 404 体：`{"message":"not found","url":"https://gitea.com/api/swagger"}`
- Gitea `GET /repos/xorm/xorm/pulls/2622.diff` → 200 `text/plain`，548 字节；`.patch` → 200，850 字节
- Gitea 忽略 `Accept: application/vnd.github.v3.diff`，返回 10521 字节 JSON
- Gitea `GET /repos/gitea/tea/raw/README.md` → 200 `text/plain`
- Gitea `GET /token` 无认证 → 401 `{"message":"token is required"}`
- GitHub `GET /meta` → `etag: W/"935c..."`；`/rate_limit` 返回完整资源表；响应头含 `access-control-expose-headers: ETag, Link, ...`
- GitHub `Accept: application/vnd.github.v3.diff` → 200 `application/vnd.github.v3.diff`
- GitHub `/search/repositories` → `{total_count, incomplete_results, items}`

规范内确认的缺失（Gitea 侧完全不存在）：`/gists`、`/discussions`、`/codespaces`、`/search/*`、`/rate_limit`、`/meta`、`/graphql`、`/rulesets`、`/actions/caches`、`/environments`、`/deployments`、`/autolinks`、`/repos/{o}/{r}/contributors`、`/user/ssh_signing_keys`、`/actions/secrets/public-key`、`/user/actions/secrets`（列表）。