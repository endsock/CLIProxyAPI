# usage_limit_reached 冷却状态持久化方案

## 1. 问题背景

当前当某个账户命中上游错误：

```json
{
  "error": {
    "type": "usage_limit_reached",
    "plan_type": "free",
    "resets_at": 1776929061
  }
}
```

系统会把该账户标记为冷却（cooldown），避免继续被选中。

但是这个冷却状态目前只存在于运行时内存中：

- 进程重启后，冷却状态丢失
- `routing.strategy: "fill-first"` 会再次优先选中这个账户
- 直到再次命中 `usage_limit_reached`，它才重新进入冷却

目标是：

1. 把冷却状态持久化到账户 JSON 文件
2. 重启后能恢复冷却状态
3. 当前时间大于 `resets_at`（或等价的 `next_retry_after`）后，账户自动恢复可用
4. 尽量走最小改动链路

---

## 2. 当前实现链路

### 2.1 上游 429 / usage_limit_reached 解析

`D:\Project\go\CLIProxyAPI\internal\runtime\executor\codex_executor.go:692`

```go
func parseCodexRetryAfter(statusCode int, errorBody []byte, now time.Time) *time.Duration
```

这里会解析：

- `error.type == "usage_limit_reached"`
- `error.resets_at`
- `error.resets_in_seconds`

并返回一个 `retryAfter` 时长。

### 2.2 冷却状态写入运行时 Auth

`D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go:1597`

在 `MarkResult` 中，如果请求失败且状态码为 `429`，会把冷却状态写入 `Auth` / `ModelState`：

- `state.Unavailable = true`
- `state.NextRetryAfter = next`
- `state.Quota.Exceeded = true`
- `state.Quota.NextRecoverAt = next`
- 然后通过 `updateAggregatedAvailability` 汇总到：
  - `auth.Unavailable`
  - `auth.NextRetryAfter`
  - `auth.Quota`

关键位置：

- `D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go:1647`
- `D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go:1663`
- `D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go:1664`
- `D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go:1757`
- `D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go:1799`

### 2.3 Selector 如何判断账户是否还能被使用

`fill-first` 的实现位置：

`D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\selector.go:360`

```go
func (s *FillFirstSelector) Pick(...)
```

它最终调用：

`D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\selector.go:372`

```go
func isAuthBlockedForModel(auth *Auth, model string, now time.Time)
```

当前阻塞判断依赖这些运行时字段：

- `auth.Disabled`
- `auth.Status == StatusDisabled`
- `auth.Unavailable`
- `auth.NextRetryAfter`
- `auth.Quota.Exceeded`
- `auth.ModelStates[model].Unavailable`
- `auth.ModelStates[model].NextRetryAfter`
- `auth.ModelStates[model].Quota.Exceeded`

也就是说：

**如果重启后这些字段恢复不出来，`fill-first` 就会把账户重新当成可用账户。**

### 2.4 为什么重启后会丢失

虽然 `MarkResult` 里会调用持久化：

`D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go:1698`

```go
_ = m.persist(ctx, auth)
```

但文件存储实际只写 `auth.Metadata`，不会直接写 `Auth` 结构体的运行时字段。

#### Manager 持久化入口

- `D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go:824`
- `D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go:847`

```go
func (m *Manager) Update(ctx context.Context, auth *Auth)
```

#### File store 保存逻辑

`D:\Project\go\CLIProxyAPI\sdk\auth\filestore.go:41`

```go
func (s *FileTokenStore) Save(ctx context.Context, auth *cliproxyauth.Auth) (string, error)
```

它的保存方式是：

- 如果 `auth.Storage != nil`，调用 `SetMetadata(auth.Metadata)` 再 `SaveTokenToFile(path)`
- 如果只有 `auth.Metadata`，则直接把 `auth.Metadata` 写回 JSON

也就是说，**只有 `Metadata` 会落盘**。

而当前冷却相关字段存在于：

- `auth.Unavailable`
- `auth.NextRetryAfter`
- `auth.Quota`
- `auth.ModelStates`
- `auth.Status`
- `auth.StatusMessage`

这些字段默认没有镜像进 `Metadata`，所以重启后会丢失。

### 2.5 启动时如何读取 auth json

启动加载：

- `D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go:852`
- `D:\Project\go\CLIProxyAPI\sdk\auth\filestore.go:184`

```go
func (s *FileTokenStore) readAuthFile(path, baseDir string) (*cliproxyauth.Auth, error)
```

当前 `readAuthFile` 只恢复了：

- `disabled`
- `provider`
- `label`
- `metadata`

并没有恢复：

- `NextRetryAfter`
- `Quota`
- `ModelStates`
- `Unavailable`
- `StatusMessage`

管理端/热加载路径 `registerAuthFromFile` 也是同样问题：

`D:\Project\go\CLIProxyAPI\internal\api\handlers\management\auth_files.go:758`

它只额外恢复了 `last_refresh`，没有恢复冷却状态。

---

## 3. 结论

### 3.1 当前冷却状态本质上是“内存态”

虽然每次状态变化后会调用 `persist`，但由于持久化层只保存 `Metadata`，所以冷却状态实际上没有真正落盘。

### 3.2 真正需要恢复的不是原始错误文本，而是“可用性决策字段”

要让重启后 `fill-first` 继续避开冷却账户，至少要恢复这些字段：

- `auth.Unavailable`
- `auth.NextRetryAfter`
- `auth.Quota`
- `auth.Status`
- `auth.StatusMessage`
- `auth.ModelStates`

其中最关键的是：

- `ModelStates[model].Unavailable`
- `ModelStates[model].NextRetryAfter`
- `ModelStates[model].Quota.Exceeded`

因为 `isAuthBlockedForModel` 在带模型名时，优先看的是 `ModelStates`。

如果只恢复聚合字段、不恢复 `ModelStates`，则模型级冷却在 `fill-first` 下仍可能失效。

---

## 4. 推荐持久化格式

推荐不要把运行时字段直接散落到 auth 文件顶层，而是统一放进一个命名空间字段，例如：

```json
{
  "type": "codex",
  "email": "user@example.com",
  "id_token": "...",
  "cooldown_state": {
    "status": "error",
    "status_message": "quota exhausted",
    "unavailable": true,
    "next_retry_after": "2026-04-23T12:34:56Z",
    "quota": {
      "exceeded": true,
      "reason": "quota",
      "next_recover_at": "2026-04-23T12:34:56Z",
      "backoff_level": 0
    },
    "model_states": {
      "gpt-5.4-low": {
        "status": "error",
        "status_message": "quota exhausted",
        "unavailable": true,
        "next_retry_after": "2026-04-23T12:34:56Z",
        "quota": {
          "exceeded": true,
          "reason": "quota",
          "next_recover_at": "2026-04-23T12:34:56Z",
          "backoff_level": 0
        },
        "updated_at": "2026-04-17T08:00:00Z"
      }
    },
    "last_error": {
      "code": "429",
      "message": "quota exhausted",
      "http_status": 429,
      "retryable": true
    },
    "reason": "usage_limit_reached",
    "plan_type": "free",
    "resets_at": 1776929061,
    "updated_at": "2026-04-17T08:00:00Z"
  }
}
```

### 为什么推荐 `cooldown_state`

优点：

1. 不污染现有 provider 自己的元数据
2. 读写边界清晰
3. 后续 UI 可以直接展示冷却原因、恢复时间、plan_type
4. 即使以后要扩展 `resets_in_seconds`、`source_error_type` 等字段，也不用改顶层结构

---

## 5. 功能上真正必需持久化的字段

如果只从“重启后还能正确避开冷却账户，并在时间到了后自动恢复”这个目标出发，最小必需字段是：

- `cooldown_state.unavailable`
- `cooldown_state.next_retry_after`
- `cooldown_state.quota`
- `cooldown_state.model_states`

### 关于 `resets_at`

`resets_at` 本身不是 selector 的判断依据。

当前 selector 真正依赖的是：

- `NextRetryAfter`
- `Quota.NextRecoverAt`

所以：

- **功能上**：只要把绝对时间 `next_retry_after` / `next_recover_at` 落盘，就能满足恢复和自动解封
- **可观测性上**：如果你想在 JSON 中保留“原始上游信息”，可以额外保存 `reason=usage_limit_reached`、`plan_type=free`、`resets_at=1776929061`

---

## 6. 最小改动方案

### 方案总览

| 目标 | 改动点 |
|---|---|
| 冷却状态写入 JSON | 在 `persist` 前把运行时冷却字段镜像到 `auth.Metadata["cooldown_state"]` |
| 重启恢复冷却状态 | 在 `readAuthFile` / `registerAuthFromFile` 中从 `metadata.cooldown_state` 还原到 `Auth` |
| 时间到后自动恢复 | 还原时或选择前清理过期 cooldown，时间已过就不再阻塞 |
| fill-first 避免再次命中冷却账户 | 无需改策略本身，只需保证 `ModelStates` 被正确恢复 |

---

## 7. 具体改动点

### 7.1 在 `conductor.go` 中增加“运行时状态 <-> metadata”镜像辅助函数

建议新增两个 helper：

#### 1）写入 metadata

建议位置：

`D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go`

新增：

```go
func syncCooldownStateToMetadata(auth *Auth)
```

职责：

- 若账户当前存在冷却/错误状态，则把这些字段写入 `auth.Metadata["cooldown_state"]`
- 若账户已经恢复可用，则删除 `auth.Metadata["cooldown_state"]`

需要写入的内容：

- `status`
- `status_message`
- `unavailable`
- `next_retry_after`
- `quota`
- `last_error`
- `model_states`
- `updated_at`
- 可选：`reason = "usage_limit_reached"`
- 可选：`plan_type = auth.Attributes["plan_type"]`
- 可选：`resets_at`

#### 2）从 metadata 恢复运行时字段

建议新增：

```go
func restoreCooldownStateFromMetadata(auth *Auth, now time.Time)
```

职责：

- 从 `auth.Metadata["cooldown_state"]` 中恢复：
  - `auth.Unavailable`
  - `auth.Status`
  - `auth.StatusMessage`
  - `auth.NextRetryAfter`
  - `auth.Quota`
  - `auth.LastError`
  - `auth.ModelStates`
- 如果 `next_retry_after <= now`，说明冷却已过期：
  - 清空该冷却状态
  - 删除 `auth.Metadata["cooldown_state"]`
  - 让账户直接恢复为可用

### 7.2 在 `MarkResult` 里持久化前调用镜像函数

位置：

- `D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go:1597`
- `D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go:1698`

当前逻辑在更新完 `auth` 后直接：

```go
_ = m.persist(ctx, auth)
```

建议改成：

```go
syncCooldownStateToMetadata(auth)
_ = m.persist(ctx, auth)
```

这样当 429 命中冷却时，JSON 文件里会同步写入 `cooldown_state`。

### 7.3 在成功恢复时清掉 metadata 里的 cooldown_state

位置：

- `D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go:1601`
- `D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go:1744`
- `D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go:1838`

当请求成功时，现有逻辑会：

- `resetModelState(...)`
- `clearAuthStateOnSuccess(...)`

这时也应该同步清理：

```go
syncCooldownStateToMetadata(auth)
```

确保冷却过后文件里的 `cooldown_state` 被移除或变成空状态。

### 7.4 在 `filestore.go` 启动加载时恢复 cooldown_state

位置：

`D:\Project\go\CLIProxyAPI\sdk\auth\filestore.go:184`

当前 `readAuthFile(...)` 只恢复基础信息，建议在创建完 `auth` 后增加：

```go
restoreCooldownStateFromMetadata(auth, time.Now())
```

如果时间还没到，则 auth 重启后仍保持冷却。

如果时间已过，则这里直接清理掉过期冷却状态，并可在后续保存时落盘删除。

### 7.5 在 `registerAuthFromFile` 热加载路径也恢复 cooldown_state

位置：

`D:\Project\go\CLIProxyAPI\internal\api\handlers\management\auth_files.go:758`

当前这里只恢复了 `last_refresh`：

```go
lastRefresh, hasLastRefresh := extractLastRefreshTimestamp(metadata)
```

建议补充：

```go
restoreCooldownStateFromMetadata(auth, time.Now())
```

这样文件热重载、管理面板上传/替换 auth 文件时，内存里的账户状态和启动时保持一致。

### 7.6 `selector.go` 可以不改，但建议加一个兜底归一化

位置：

`D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\selector.go:372`

当前 `isAuthBlockedForModel` 已经具备“时间到了就不再阻塞”的一半逻辑：

- 如果 `state.NextRetryAfter.After(now)` 才继续阻塞
- 如果时间已过，会把：
  - `state.Unavailable = false`
  - `state.NextRetryAfter = time.Time{}`

这意味着：

**只要 `ModelStates` 被正确恢复，当前时间大于 `resets_at` / `next_retry_after` 后，账户已经可以再次被使用。**

但它现在不会同步清理：

- `state.Quota.Exceeded`
- `auth.Quota.Exceeded`
- `metadata.cooldown_state`

所以建议额外补一个统一函数，例如：

```go
func normalizeExpiredCooldown(auth *Auth, now time.Time) bool
```

在以下位置复用：

- `restoreCooldownStateFromMetadata`
- `isAuthBlockedForModel`

保证：

- 时间到了 -> 内存态清干净
- 下次 `persist` -> 文件里的 `cooldown_state` 也能被清掉

---

## 8. 是否需要修改 `parseCodexRetryAfter`

### 如果只追求功能恢复：不需要

因为现有链路已经把 `resets_at` / `resets_in_seconds` 转成了 `RetryAfter`，再由 `MarkResult` 算成 `next_retry_after`。

只要把这个绝对时间持久化下来，就够了。

### 如果希望 JSON 里保留原始字段：建议增强

如果希望 auth json 里不仅能恢复逻辑，还能看到：

- `type = usage_limit_reached`
- `plan_type = free`
- `resets_at = 1776929061`

那就需要把当前只返回 `*time.Duration` 的逻辑升级成结构化结果。

建议方向：

把：

```go
func parseCodexRetryAfter(...) *time.Duration
```

扩展为类似：

```go
type CodexCooldownHint struct {
    ErrorType  string
    RetryAfter time.Duration
    ResetsAt   time.Time
    PlanType   string
}
```

然后把这部分信息透传到 `Result`，最后写入 `metadata.cooldown_state`。

### 建议取舍

- **最小改动**：不改 `parseCodexRetryAfter`，只持久化最终的 `next_retry_after`
- **增强可观测性**：再升级为结构化 cooldown hint，并把 `usage_limit_reached / plan_type / resets_at` 原样落盘

---

## 9. 推荐实施顺序

### 第一步：先做“功能闭环”

只实现这些：

1. `syncCooldownStateToMetadata(auth)`
2. `restoreCooldownStateFromMetadata(auth, now)`
3. 在 `MarkResult` 持久化前写入 metadata
4. 在 `readAuthFile` / `registerAuthFromFile` 恢复 metadata
5. 时间过期后自动清理并恢复可用

这一步完成后，已经能解决：

- 重启丢冷却
- fill-first 再次打到冷却账户
- 到时间后自动恢复

### 第二步：再补“原始上游信息可观测性”

再考虑把这些原始字段也保留下来：

- `type = usage_limit_reached`
- `plan_type = free`
- `resets_at`

---

## 10. 验证点

建议至少补这几类测试：

### 10.1 启动恢复测试

场景：

- 某 codex auth 已经写入 `cooldown_state`
- `next_retry_after` 晚于当前时间
- 重启 / `Manager.Load()` 后

期望：

- `fill-first` 不会选中它
- `GetByID()` 拿到的 `Auth` 中 `ModelStates` 与 `Quota` 已恢复

### 10.2 过期自动恢复测试

场景：

- auth json 中有 `cooldown_state`
- `next_retry_after` 早于当前时间

期望：

- 加载后账户直接可用
- 过期冷却状态被清掉
- 下次持久化时文件中的 `cooldown_state` 被移除

### 10.3 模型级冷却测试

场景：

- 只对某个 model 写入 `model_states[model]`
- 请求该 model

期望：

- `fill-first` 会跳过这个 auth
- 请求其他未冷却 model 时，不会误伤

### 10.4 管理面板热更新测试

场景：

- 管理接口上传/修改 auth json
- watcher/`registerAuthFromFile` 触发热更新

期望：

- 新的 cooldown_state 能立即恢复到内存
- 选择逻辑与冷启动一致

---

## 11. 最终建议

### 推荐采用的最终方案

#### 必做

1. 在 `conductor.go` 中把运行时冷却状态镜像进 `auth.Metadata["cooldown_state"]`
2. 在 `filestore.go` 和 `registerAuthFromFile` 中恢复 `cooldown_state`
3. 恢复时如果当前时间已经大于 `next_retry_after` / `quota.next_recover_at`，直接清掉冷却并恢复账户可用
4. **一定要恢复 `ModelStates`**，否则 `fill-first` 在带模型路由时仍可能误选

#### 可选增强

5. 扩展 `parseCodexRetryAfter` 为结构化 cooldown hint，把 `usage_limit_reached / plan_type / resets_at` 原样写入 `cooldown_state`

---

## 12. 涉及文件清单

### 必改

- `D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\conductor.go`
- `D:\Project\go\CLIProxyAPI\sdk\auth\filestore.go`
- `D:\Project\go\CLIProxyAPI\internal\api\handlers\management\auth_files.go`

### 可能增强

- `D:\Project\go\CLIProxyAPI\internal\runtime\executor\codex_executor.go`
- `D:\Project\go\CLIProxyAPI\sdk\cliproxy\auth\selector.go`

---

## 13. 一句话总结

**最小可行方案不是去改 `fill-first`，而是把当前已经存在于 `Auth` / `ModelStates` 里的冷却状态镜像到 `auth.Metadata` 并持久化到 JSON，再在加载时恢复；这样重启后仍能避开冷却账户，并且当当前时间大于 `resets_at` 对应的 `next_retry_after` 后，账户会自动重新进入可用集合。**
