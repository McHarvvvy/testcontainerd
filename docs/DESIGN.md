# testcontainerd 设计文档

> 本文档面向 testcontainerd 的维护者和深度使用者，旨在阐明系统的逻辑架构、进程管理时序和关键技术决策。

---

## 1. 设计目标

testcontainerd 要解决的核心问题是：**当 `go test ./...` 并发运行几十个测试包时，如何让所有测试进程共享同一组容器和被测服务，而不是每个包各拉一套？**

由此推导出三个设计目标：

| 目标         | 约束                                                         |
| :----------- | :----------------------------------------------------------- |
| **复用**     | 多个测试进程必须共享同一组容器实例，不能重复创建             |
| **并发安全** | `go test ./...` 本质是多个独立进程并发启动，不能依赖进程内锁 |
| **自动回收** | 测试结束后容器、SUT、daemon 进程必须自动清理，不能残留       |

---

## 2. 逻辑架构

### 2.1 总体分层

系统分为 **入口层 → 协调层 → 基础设施层** 三层，每一层的关注点严格分离：

```
┌──────────────────────────────────────────────────────────────────────┐
│                          入口层 (testcontainerd.go)                  │
│     TestContainerd.Run(m) ──▶ 环境变量判断 ──▶ client / daemon       │
└──────────────────────┬────────────────────────────┬──────────────────┘
                       │                            │
          ┌────────────▼────────────┐   ┌───────────▼──────────────┐
          │      Client 协调层       │   │      Daemon 协调层        │
          │  client/client.go        │   │  daemon/server.go         │
          │  client/autostart.go     │   │  daemon/lifecycle.go      │
          │  client/heartbeat.go     │   │  daemon/lease_store.go    │
          │                          │   │  daemon/sut_manager.go    │
          │  职责：                    │   │  daemon/cleanup.go       │
          │  · 发现或自启动 daemon     │   │                           │
          │  · 获取/释放 lease         │   │  职责：                    │
          │  · 心跳保活               │   │  · HTTP API 服务           │
          └──────────┬───────────────┘   │  · lease 管理              │
                     │                   │  · 容器/SUT 生命周期编排    │
                     │ HTTP              │  · 空闲回收                 │
                     └───────────────────┘  └────────┬─────────────────┘
                                                     │
          ┌──────────────────────────────────────────┼────────────────────────────┐
          │                        基础设施层                                      │
          │                                                                       │
          │  container/               protocol/        tcdruntime/                │
          │  ├── registry.go          types.go         file.go (runtime R/W)      │
          │  ├── bundle.go            const.go         path.go (路径推导)          │
          │  ├── drivers.go           error.go         log.go (日志管理)           │
          │  ├── options.go                                                       │
          │  ├── runtime_view.go                                                  │
          │  └── spec/                                                            │
          │      ├── mysql.go                                                     │
          │      ├── redis.go                                                     │
          │      ├── mongo.go                                                     │
          │      └── pulsar.go                                                    │
          └───────────────────────────────────────────────────────────────────────┘
```

### 2.2 组件职责

**入口层** — `testcontainerd.go`

- 唯一对外暴露的公开 API：`New()` 和 `Run(m)`。
- 通过环境变量 `TCD_MODE` 做 **进程角色分叉**：无环境变量走 client 路径，`TCD_MODE=daemon` 走 daemon 路径。
- 将用户提供的 `Config` 标准化后分发给 client 和 daemon 子模块。

**Client 协调层** — `client/`

- `client.go`：封装 daemon HTTP API 的调用（Acquire / Heartbeat / Release / State）。
- `autostart.go`：当 runtime 文件不可用时，以 daemon 模式重启当前测试二进制。使用文件锁协调并发。
- `heartbeat.go`：后台 goroutine 定期续租。

**Daemon 协调层** — `daemon/`

- `server.go`：HTTP 服务、路由注册、鉴权中间件、reaper 循环。
- `lifecycle.go`：容器启动/停止入口，串行化保护。
- `lease_store.go`：内存 lease 存储，提供 acquire / heartbeat / release / gc。
- `sut_manager.go`：SUT 进程的启动、就绪探测、停止、状态查询。
- `cleanup.go`：daemon 冷启动前清理同名残留容器。

**基础设施层**

- `container/`：容器配置注册（Registry）、实例生命周期编排（Bundle）、Docker 交互（drivers）、运行时视图。
- `container/spec/`：驱动抽象——每种容器类型的默认端口、环境变量、就绪策略、URI 生成规则。
- `protocol/`：client-daemon 间 HTTP 协议的请求/响应结构体与常量。
- `tcdruntime/`：runtime 发现文件的读写/等待、路径推导、日志文件管理。
- `constant/`：全局常量（容器类型、端口名、环境变量键、租约参数）。

### 2.3 依赖方向

```
testcontainerd.go
  ├──▶ client/*      ──▶ tcdruntime, protocol, constant
  ├──▶ daemon/*      ──▶ container/*, tcdruntime, protocol, constant
  └──▶ container/*   ──▶ container/spec, protocol, constant
                          └──▶ testcontainers-go (第三方)
                          └──▶ docker/client     (第三方)
```

关键约束：**client 不依赖 daemon，daemon 不依赖 client**。两者仅通过 `protocol/` 定义的 HTTP 协议通信。

---

## 3. 进程管理时序

### 3.1 总体流程（单进程视角）

```
TestMain(m)
    │
    ▼
testcontainerd.New(cfg, hook)        ← 创建 TestContainerd 实例
    │
    ▼
tcd.Run(m)
    │
    ├── TCD_MODE == "daemon"?
    │       │
    │       ├── YES → runDaemonMode()
    │       │         ① 执行 registerHook，注册容器配置
    │       │         ② daemon.New(cfg, registry)
    │       │         ③ daemon.Start(ctx)     ← 阻塞直到退出
    │       │            ├── 清理同名残留容器
    │       │            ├── 监听 TCP 端口
    │       │            ├── 写入 runtime.json
    │       │            ├── 启动 reaper 循环
    │       │            └── 启动 HTTP 服务 ← Serve 阻塞
    │       │
    │       └── NO → runClientMode(m)
    │                 ① client.New(ctx, cfg)
    │                 │    └── connectOrStart()
    │                 │         ├── 尝试读 runtime.json 并探活
    │                 │         ├── 若不可达 → autoStartDaemon()
    │                 │         │    ├── 文件锁竞争
    │                 │         │    ├── 构建 daemon 命令
    │                 │         │    ├── 启动子进程（TCD_MODE=daemon）
    │                 │         │    └── 等待 runtime.json 就绪
    │                 │         └── 连接 daemon
    │                 ② client.Acquire(ctx)   → POST /v1/acquire
    │                 │    daemon 端处理：
    │                 │    ├── ensureInfraStarted()  → Bundle.StartAll()
    │                 │    ├── ensureSUTStarted()    → SUT 编译/启动/探测
    │                 │    └── leaseStore.acquire()  → 分配 lease
    │                 ③ client.StartHeartbeat(leaseID)
    │                 ④ SUT.SetEnvEndpoint()    ← 注入 SUT 地址到环境变量
    │                 ⑤ m.Run()                ← 执行所有测试用例
    │                 ⑥ stopHeartbeat()
    │                 ⑦ client.Release(ctx, leaseID, exitCode)
    │                 └── return exitCode
```

### 3.2 并发测试场景（多进程视角）

当 `go test -count=1 ./...` 同时启动 N 个测试包时：

```
时间轴 ──────────────────────────────────────────────────────────────▶

进程A │ New → Run → client.New → 读 runtime ✗ → 抢锁 ✓ → 启动 daemon 子进程
      │                                                      │
      │                                          等待 runtime.json ───┐
      │                                                              │
进程B │ New → Run → client.New → 读 runtime ✗ → 抢锁 ✗ ──────────────┤
      │                                                等待 runtime  │
      │                                                              │
进程C │ New → Run → client.New → 读 runtime ✗ → 抢锁 ✗ ──────────────┤
      │                                                              │
      │                                                              │
daemon│ ......启动中......写 runtime.json...... HTTP ready ◀───────────┘
      │                                           │
      │                                           ▼
进程A │ ◀─── 读 runtime ✓ ─── 探活 ✓ ─── Acquire ─── 拿 lease ─── Run tests
进程B │ ◀─── 读 runtime ✓ ─── 探活 ✓ ─── Acquire ─── 拿 lease ─── Run tests
进程C │ ◀─── 读 runtime ✓ ─── 探活 ✓ ─── Acquire ─── 拿 lease ─── Run tests
      │                                           │
      │ （daemon 端首次 Acquire 触发容器启动 +      │
      │   SUT 启动，后续 Acquire 直接返回）         │
      │                                           │
进程A │ tests done → stopHB → Release ─────────┐  │
进程B │ tests done → stopHB → Release ─────────┤  │
进程C │ tests done → stopHB → Release ─────────┤  │
      │                                        ▼  │
daemon│         leases=0 → 空闲计时开始                │
      │         idle > SUT.IdleTTL → 停止 SUT        │
      │         idle > Daemon.IdleTTL → 停止容器 → 清理 runtime → 退出
```

### 3.3 Daemon 内部：Acquire 请求处理时序

```
handleAcquire(w, r)
    │
    ├── acquireInFlight.Add(1)        ← 标记请求进行中，抑制 reaper 误判
    │
    ├── ensureInfraStarted(ctx)       ← startMu 串行化
    │   │
    │   ├── 已启动? → 返回 endpoints
    │   └── 未启动? → Bundle.StartAll(ctx)
    │       │
    │       ├── Registry.Snapshot()   ← 获取冻结后的配置快照
    │       ├── 并发启动容器 (max 4 workers)
    │       │   ├── buildContainerRequest() → spec.Driver
    │       │   └── createContainerWithRetry() → testcontainers-go
    │       │       └── 重试逻辑：仅对可恢复冲突重试，不掩盖配置错误
    │       │
    │       ├── 任一失败 → terminateCreatedContainers → 返回错误
    │       │
    │       └── 全部成功 → 按注册顺序执行 Init 函数
    │           │
    │           ├── InitInput{Self, RuntimeView}
    │           │   RuntimeView 在构造时深拷贝，避免并发读写
    │           │
    │           └── Init 失败 → rollback（销毁全部容器）→ 返回错误
    │
    ├── ensureSUTStarted(ctx, endpoints)
    │   │
    │   ├── 已运行? → 返回
    │   └── 未运行? →
    │       ├── boot.GetCommand(ctx, in) → *exec.Cmd
    │       ├── validateCommand(cmd)     → 检查 Path、Dir
    │       ├── prepareSUTCommand(cmd)   → 平台相关（Unix: Setpgid / Win: noop）
    │       ├── cmd.Start()
    │       ├── afterSUTStart(cmd)       → 平台相关（Win: 创建 JobObject）
    │       └── waitSUTReady(ctx, waitCh, probeAddrs, timeout)
    │           ├── 有探测地址 → 轮询 TCP dial 直到全部可达
    │           └── 无探测地址 → 等待 400ms 确认进程未立即退出
    │
    ├── leaseStore.acquire(pid, ttl)  ← 分配新 lease
    │
    └── acquireInFlight.Add(-1)       ← 标记请求结束
```

### 3.4 Daemon 空闲回收时序

```
loopReaper() ─── 每 2s 执行一次 ──────────────────────────────────────▶

tick │ activeCount > 0?      → 重置 idleSince + sutIdleSince
     │ acquireInFlight > 0?  → 重置 idleSince + sutIdleSince
     │
     │ 全部空闲:
     │   ├── sutIdleSince 未设 → 初始化 sutIdleSince = now
     │   ├── now - sutIdleSince ≥ SUT.IdleTTL → stopSUTOnly()
     │   │
     │   ├── idleSince 未设   → 初始化 idleSince = now
     │   └── now - idleSince ≥ Daemon.IdleTTL → shutdownServer()
     │       │
     │       └── shutdownOnce.Do:
     │           ├── close(done)           ← 通知 reaper 退出
     │           ├── stopSUTOnly()         ← 先停 SUT
     │           ├── stopContainersOnly()  ← 再停容器
     │           ├── httpServer.Shutdown() ← 关闭 HTTP
     │           └── tcdruntime.Remove()   ← 删除 runtime.json
```

---

## 4. 关键技术决策

本节精选对系统架构和正确性影响最大的核心决策，阐述其背景与权衡。

### 4.1 "自己启动自己"的 Daemon 拉起策略

**决策**：client 发现无可用 daemon 时，以 daemon 模式 **重启当前测试二进制** 来拉起守护进程（`client/autostart.go` `buildDaemonCommand`）。

**为什么不用其他方案**：
- ~~独立二进制~~：需要额外的构建与分发步骤，增加接入成本。
- ~~`runtime.Caller` 推导源码路径后 `go run`~~：在不同编译参数（`-trimpath`）和 CI 执行器下路径不稳定。

当前方案零额外构建步骤，任何 `go test` 产物都能直通 daemon 模式。Windows 上需要先复制二进制避免文件锁（`prepareDaemonExecutable`），这是唯一的平台特化处理。

### 4.2 文件锁解决跨进程并发启动

**决策**：通过 `O_CREATE|O_EXCL` 原子创建 `.start.lock` 文件实现跨进程排他（`client/autostart.go` `tryAcquireStartLock`）。

**核心问题**：`go test ./...` 并发启动 N 个独立进程，进程内互斥锁无效，必须依赖文件系统级原子操作。

**降级策略**：
- 未抢到锁的进程轮询等待 `runtime.json` 出现即可连接。
- 陈旧锁判定要求 **"锁超时（90s）+ runtime 不存在"双条件**，避免正常慢启动期间误删锁导致并发启动多个 daemon。
- Start 失败不等于最终失败——并发场景下另一个进程可能已经拉起 daemon，再等待一次 runtime 作为兜底收敛。

### 4.3 Acquire 串联"容器就绪 + SUT 就绪"

**决策**：`handleAcquire` 内部串联 `ensureInfraStarted` → `ensureSUTStarted` → `leaseStore.acquire`，三步全部成功才返回 lease（`daemon/server.go`）。

**设计意图**：用例拿到 lease 时，一定是可用环境而非半初始化状态。首次 Acquire 触发容器拉起和 SUT 编译/启动，后续 Acquire 直接返回缓存结果。

**配套保护**：`acquireInFlight` 原子计数器抑制空闲回收误判——Acquire 正在执行但 lease 尚未落库时 `activeCount=0`，计数器阻止 reaper 在此窗口触发退出。

### 4.4 双层空闲回收策略

**决策**：SUT 与 daemon 使用独立的空闲计时器，分阶段回收（`daemon/server.go` `loopReaper`）。

```
lease 全部释放 → 等待 SUT.IdleTTL → 停止 SUT（释放端口）
                → 等待 Daemon.IdleTTL → 停止容器 → 退出 daemon
```

**设计意图**：容器拉起成本高（拉镜像 + 等待就绪），SUT 重启成本低（重新编译即可）。因此让容器长时间保持可用，SUT 尽快释放端口方便下一轮重新编译部署。

**关闭顺序**：先停 SUT，再停容器——避免 SUT 在依赖容器先下线后产生大量无意义报错日志。

### 4.5 容器编排：并发启动 + Init 后置 + 失败全量回滚

三项决策共同构成容器编排的核心行为（`container/bundle.go`）：

1. **并发启动**（最多 4 worker）：加速多容器场景；任一容器失败立即 cancel 其余并终止已创建的容器。
2. **Init 后置**：所有容器全部启动后再统一执行 `InitFunc`，确保初始化逻辑可通过 `RuntimeView` 访问完整的运行时信息（如某个 Init 需要查询另一个容器的映射端口）。
3. **Init 失败全量回滚**：保证下次 Acquire 仍从干净环境开始，不会残留脏状态的容器。

此外，容器名直接使用实例配置名（`container/drivers.go`），daemon 冷启动前会清理同名残留容器（`daemon/cleanup.go`），清理后轮询 Docker Inspect 直到 `NotFound` 消除 create/remove 竞态窗口。

### 4.6 跨平台进程树回收

**核心问题**：SUT 可能 fork 子进程（如 shell 脚本拉起后台服务），仅 kill 主进程会导致子进程树泄漏。

| 平台        | 方案                                                                                                                         | 代码位置                            |
| :---------- | :--------------------------------------------------------------------------------------------------------------------------- | :---------------------------------- |
| **Unix**    | `Setpgid=true` 将 SUT 放入独立进程组，终止时 `kill(-pgid, SIGKILL)` 一次性杀掉整棵进程树                                     | `daemon/process_control_unix.go`    |
| **Windows** | 创建 JobObject 并设置 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`，句柄关闭时自动清理所有子进程；再用 `taskkill /T /F` 做系统级兜底 | `daemon/process_control_windows.go` |



## 5. Runtime 文件发现机制

runtime 文件是 daemon 与 client 之间唯一的发现通道。

### 5.1 文件格式

`runtime.json` 是一个 JSON 文件，内容对应 `protocol.RuntimeInfo`：

```json
{
  "addr": "127.0.0.1:54321",
  "token": "1740000000000000000",
  "pid": 12345,
  "started_at": "2026-02-27T15:00:00Z"
}
```

### 5.2 写入原子性

`tcdruntime.Write` 采用 **先写 tmp 再 rename** 的方式保证原子性，避免 client 读到半写入的文件内容。

### 5.3 路径隔离

不同项目通过 `Project` 字段隔离到不同的 runtime 目录：

```
{os.TempDir()}/tcd/{project}/runtime.json
```

项目名中的特殊字符（`\`、`/`、`:`、空格）会被替换为 `_`，确保路径合法。

---

## 6. 安全边界

| 层面         | 策略                                                                             |
| ------------ | -------------------------------------------------------------------------------- |
| **网络**     | daemon 仅监听 `127.0.0.1`，不暴露到外部网络                                      |
| **认证**     | 每次请求要求 `X-Testcontainerd-Token` 头匹配，token 默认使用纳秒级时间戳自动生成 |
| **进程隔离** | runtime 文件按 Project 隔离，多项目并行时互不干扰                                |
| **资源回收** | lease TTL + 心跳 + reaper + runtime 清理多层兜底，确保即使异常退出也不会永久残留 |

---

## 7. 限制与约束

- **单机部署**：daemon 默认只监听 loopback，不支持跨机器共享容器。
- **Docker 依赖**：底层依赖 `testcontainers-go`，要求本机 Docker Engine 可用。
- **无持久化**：lease 存储在内存中，daemon 重启后所有 lease 丢失（设计如此，重启意味着环境重建）。
- **容器名全局唯一**：容器名与实例配置名绑定，同一 Docker host 上不能有手工创建的同名容器。
- **SUT 单实例**：daemon 同一时刻只管理一个 SUT 进程，不支持多 SUT 并行。
