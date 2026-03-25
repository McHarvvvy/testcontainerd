# testcontainerd 设计文档

> 本文档面向 testcontainerd 的维护者和深度使用者，阐明系统的运行机制、公开接口契约和关键技术决策。

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
┌──────────────────────────────────────────────────────────────┐
│                          入口层                               │
│   TestContainerd.Run(m) ──▶ TCD_MODE 判断 ──▶ client/daemon  │
└──────────────────────┬──────────────────────┬────────────────┘
                       │                      │
          ┌────────────▼────────────┐   ┌─────▼─────────────────┐
          │      Client 协调层       │   │     Daemon 协调层       │
          │                         │   │                         │
          │  · 发现或自启动 daemon   │   │  · HTTP API 服务         │
          │  · 获取 / 释放 lease     │   │  · lease 管理            │
          │  · 心跳保活              │   │  · 容器/SUT 生命周期编排  │
          │  · 执行测试代码           │   │  · 空闲回收               │
          └──────────┬──────────────┘   └──────────┬─────────────┘
                     │         HTTP                 │
                     └──────────────────────────────┘
                                     │
          ┌──────────────────────────┼───────────────────────────┐
          │                     基础设施层                         │
          │                                                        │
          │   container/    protocol/    tcdruntime/   constant/   │
          └────────────────────────────────────────────────────────┘
```

### 2.2 组件职责

**入口层** — `testcontainerd.go`

- 唯一对外暴露的公开 API：`New()` 和 `Run(m)`。
- 通过环境变量 `TCD_MODE` 做 **进程角色分叉**：无环境变量走 client 路径，`TCD_MODE=daemon` 走 daemon 路径。
- 将用户提供的 `Config` 标准化后分发给 client 和 daemon 子模块。

**Client 协调层** — `client/`

发现或自启动 daemon，申请 lease，后台心跳保活，执行测试（`m.Run()`），释放 lease。

**Daemon 协调层** — `daemon/`

提供 HTTP API，管理 lease 生命周期，编排容器和 SUT 进程的启动/停止，在空闲超时后自动回收资源并退出。

**基础设施层**

- `container/`：`ContainerRegistration` 定义与 `Bundle` 编排（并发 Start、顺序 Init、失败回滚）。
- `protocol/`：client-daemon HTTP 协议的请求/响应结构体与路径常量。
- `tcdruntime/`：runtime 发现文件的读写、路径推导、日志文件管理。
- `constant/`：租约参数、环境变量键等全局常量。

### 2.3 依赖方向

```
testcontainerd.go
  ├──▶ client/*    ──▶ tcdruntime, protocol, constant
  ├──▶ daemon/*    ──▶ container/*, tcdruntime, protocol, constant
  └──▶ container/* ──▶ testcontainers-go, docker/client (第三方)
```

关键约束：**client 不依赖 daemon，daemon 不依赖 client**。两者仅通过 `protocol/` 定义的 HTTP 协议通信。

---

## 3. 公开接口设计

本节列出调用方可见的全部类型与函数，作为接口契约的参考依据。

### 3.1 入口配置

```go
// 顶层配置，传入 New()
type Config struct {
    Global GlobalConfig
    Daemon DaemonConfig
    Client ClientConfig
    SUT    SUTBootPlan  // 可选，nil 表示不托管 SUT
}

type GlobalConfig struct {
    Project     string // 项目标识，决定 runtime 文件隔离路径；空值默认 "default"
    RuntimePath string // runtime.json 完整路径；空值按 Project 自动推导
}

type DaemonConfig struct {
    Addr    string        // daemon 监听地址，推荐 "127.0.0.1:0"（随机端口）
    Token   string        // 鉴权令牌；空值自动生成
    IdleTTL time.Duration // 无活跃 lease 后自动退出的等待时间
}

type ClientConfig struct {
    HTTPTimeout time.Duration // 测试进程请求 daemon 的超时
}
```

### 3.2 容器注册

```go
// RegisterContainersFunc 由调用方实现，返回所有需要共享的容器注册项。
type RegisterContainersFunc func(ctx context.Context) ([]container.ContainerRegistration, error)

// ContainerRegistration 描述单个容器的启动方式与初始化逻辑。
type ContainerRegistration struct {
    Name  string                                                     // 唯一标识，同时作为容器名
    Start func(ctx context.Context) (testcontainers.Container, error) // 使用 testcontainers-go 标准 API
    Init  func(ctx context.Context) error                            // 可选；所有容器启动后执行
}
```

`Init` 在所有容器全部就绪后按注册顺序执行，适合建表、写种子数据等跨容器初始化操作。

### 3.3 SUT 接口

被测服务（SUT）由调用方实现 `SUTBootPlan` 接口，传入 `Config.SUT`：

```go
type SUTBootPlan interface {
    IsEnable() bool                                                // 是否启用 SUT 管理
    GetIdleTTL() time.Duration                                     // 无活跃 lease 后停止 SUT 的等待时间
    GetReadyTimeout() time.Duration                                // 就绪探测超时
    GetGracePeriod() time.Duration                                 // 停止时的优雅退出等待时长
    GetProbeAddrs() []string                                       // 就绪探测地址列表；nil 表示只等进程不立即退出
    GetCommand(ctx context.Context, in StartSUTInput) (*exec.Cmd, error) // 构造 SUT 启动命令
}

// StartSUTInput 在调用 GetCommand 时传入，提供项目上下文。
type StartSUTInput struct {
    Project     string
    RuntimePath string
}
```

`GetCommand` 是注入容器连接信息的唯一时机——调用方在此通过 Docker API 查询映射端口，写入 `cmd.Env`。

### 3.4 运行入口

```go
// Runnable 由 *testing.M 天然满足。
type Runnable interface {
    Run() int
}

func New(cfg Config, registerContainers RegisterContainersFunc) (*TestContainerd, error)
func (t *TestContainerd) Run(m Runnable) int
```

`Run` 通过 `TCD_MODE` 环境变量判断进程角色：未设置走 client 路径，`TCD_MODE=daemon` 走 daemon 路径。

---

## 4. 进程管理时序

### 4.1 Daemon 启动机制与 TestMain 的作用

**Daemon 以"自启动"方式运行**：当首个测试进程发现无可用 daemon 时，client 会将**当前测试二进制**以子进程方式重新执行，同时注入 `TCD_MODE=daemon` 和 `TCD_RUNTIME=<路径>` 环境变量，并传入 `-test.run=^$` 跳过测试函数。

子进程启动后走同一个 `TestMain` → `tcd.Run(m)`，`Run` 检测到 `TCD_MODE=daemon` 后进入 daemon 初始化路径，启动 HTTP 服务并阻塞。

**这正是 TestMain 不可省略的根本原因**：没有 TestMain，`tcd.Run(m)` 就不会被调用，重启的子进程找不到 daemon 初始化入口，直接退出，框架无法工作。

所有传给原进程的环境变量（包括 `TCD_SCENARIO` 等用户自定义变量）都会被子进程继承，这是测试进程与 daemon 进程共享配置的机制基础。

### 4.2 总体流程

```
TestMain(m)
    │
    ▼
tcd.New(cfg, registerFn) → tcd.Run(m)
    │
    ├── TCD_MODE=daemon ──▶ daemon.Start(ctx)  ← 监听 HTTP，阻塞直到空闲退出
    │
    └── (默认) client 模式:
            ① 发现或启动 daemon（文件锁 + re-exec 当前二进制）
            ② Acquire lease → 触发容器启动 + SUT 启动（首次）
            ③ 心跳保活 → m.Run() → 停止心跳 → Release
```

### 4.3 并发测试场景（多进程视角）

`go test ./...` 并发启动 N 个测试包进程：

```
时间轴 ─────────────────────────────────────────────────────────────▶

进程A │ tcd.Run → 抢到文件锁 → re-exec 启动 daemon 子进程
进程B │ tcd.Run → 未抢到锁 → 等待 runtime.json 出现
进程C │ tcd.Run → 未抢到锁 → 等待 runtime.json 出现

daemon│ 启动 → 写 runtime.json → HTTP 就绪
      │
进程A │ Acquire → 触发容器 + SUT 启动 → 拿到 lease → 执行测试
进程B │ Acquire → 容器已就绪（复用）  → 拿到 lease → 执行测试
进程C │ Acquire → 容器已就绪（复用）  → 拿到 lease → 执行测试

进程A │ 测试完成 → Release
进程B │ 测试完成 → Release
进程C │ 测试完成 → Release

daemon│ leases=0 → 空闲计时 → 停止 SUT → 停止容器 → 退出
```

### 4.4 Daemon 内部：Acquire 处理

```
POST /v1/acquire
    ├── [acquireInFlight++]  抑制 reaper 在此窗口误触发
    ├── ensureInfraStarted   首次触发，串行化保护
    │     未启动 → Bundle.StartAll：
    │       · 最多 4 个 worker 并发 Start
    │       · 任一失败 → 逆序停止已创建容器（全量回滚）
    │       · 全部成功 → 按注册顺序执行 Init(ctx)
    │       · Init 失败 → 逆序停止全部容器（全量回滚）
    ├── ensureSUTStarted     仅在启用 SUT 时触发
    │     未运行 → GetCommand → Start
    │       · 有 probeAddrs：轮询 TCP dial 直到全部可达
    │       · 无 probeAddrs：短暂等待确认进程未立即退出
    ├── leaseStore.acquire   分配 LeaseID 并返回
    └── [acquireInFlight--]
```

### 4.5 Daemon 空闲回收

```
每 2s 检查一次：
  · 有活跃 lease 或 Acquire 正在执行 → 重置空闲计时
  · 空闲时长 ≥ SUT.IdleTTL          → 停止 SUT
  · 空闲时长 ≥ Daemon.IdleTTL       → 停止容器 → 删除 runtime.json → 退出
```

---

## 5. 关键技术决策

本节精选对系统架构和正确性影响最大的核心决策，阐述其背景与权衡。

### 5.1 "自己启动自己"的 Daemon 拉起策略

**决策**：client 发现无可用 daemon 时，以 daemon 模式 **重启当前测试二进制** 来拉起守护进程（`client/autostart.go` `buildDaemonCommand`）。

**为什么不用其他方案**：
- ~~独立二进制~~：需要额外的构建与分发步骤，增加接入成本。
- ~~`runtime.Caller` 推导源码路径后 `go run`~~：在不同编译参数（`-trimpath`）和 CI 执行器下路径不稳定。

当前方案零额外构建步骤，任何 `go test` 产物都能直通 daemon 模式。Windows 上需要先复制二进制避免文件锁（`prepareDaemonExecutable`），这是唯一的平台特化处理。

### 5.2 文件锁解决跨进程并发启动

**决策**：通过 `O_CREATE|O_EXCL` 原子创建 `.start.lock` 文件实现跨进程排他（`client/autostart.go` `tryAcquireStartLock`）。

**核心问题**：`go test ./...` 并发启动 N 个独立进程，进程内互斥锁无效，必须依赖文件系统级原子操作。

**降级策略**：
- 未抢到锁的进程轮询等待 `runtime.json` 出现即可连接。
- 陈旧锁判定要求 **"锁超时（90s）+ runtime 不存在"双条件**，避免正常慢启动期间误删锁导致并发启动多个 daemon。
- Start 失败不等于最终失败——并发场景下另一个进程可能已经拉起 daemon，再等待一次 runtime 作为兜底收敛。

### 5.3 Acquire 串联"容器就绪 + SUT 就绪"

**决策**：`handleAcquire` 内部串联 `ensureInfraStarted` → `ensureSUTStarted` → `leaseStore.acquire`，三步全部成功才返回 lease（`daemon/server.go`）。

**设计意图**：用例拿到 lease 时，一定是可用环境而非半初始化状态。首次 Acquire 触发容器拉起和 SUT 启动，后续 Acquire 直接返回缓存结果。

**配套保护**：`acquireInFlight` 原子计数器抑制空闲回收误判——Acquire 正在执行但 lease 尚未落库时 `activeCount=0`，计数器阻止 reaper 在此窗口触发退出。

### 5.4 双层空闲回收策略

**决策**：SUT 与 daemon 使用独立的空闲计时器，分阶段回收（`daemon/server.go` `loopReaper`）。

```
lease 全部释放 → 等待 SUT.IdleTTL → 停止 SUT（释放端口）
                → 等待 Daemon.IdleTTL → 停止容器 → 退出 daemon
```

**设计意图**：容器拉起成本高（拉镜像 + 等待就绪），SUT 重启成本低（重新编译即可）。因此让容器长时间保持可用，SUT 尽快释放端口方便下一轮重新编译部署。

**关闭顺序**：先停 SUT，再停容器——避免 SUT 在依赖容器先下线后产生大量无意义报错日志。

### 5.5 容器编排：并发启动 + Init 后置 + 失败全量回滚

三项决策共同构成容器编排的核心行为（`container/bundle.go`）：

1. **并发启动**（最多 4 worker）：加速多容器场景；任一容器失败立即终止已创建的容器。
2. **Init 后置**：所有容器全部启动后再按注册顺序执行 `Init(ctx)`，确保初始化逻辑可访问任意容器的运行时信息（如映射端口）。
3. **Init 失败全量回滚**：保证下次 Acquire 仍从干净环境开始，不会残留脏状态的容器。

此外，容器名直接使用注册项的 `Name` 字段，daemon 冷启动前会清理同名残留容器（`daemon/cleanup.go`），清理后轮询 Docker Inspect 直到 `NotFound` 消除 create/remove 竞态窗口。

### 5.6 跨平台进程树回收

**核心问题**：SUT 可能 fork 子进程（如 shell 脚本拉起后台服务），仅 kill 主进程会导致子进程树泄漏。

| 平台        | 方案                                                                                                                         | 代码位置                            |
| :---------- | :--------------------------------------------------------------------------------------------------------------------------- | :---------------------------------- |
| **Unix**    | `Setpgid=true` 将 SUT 放入独立进程组，终止时 `kill(-pgid, SIGKILL)` 一次性杀掉整棵进程树                                     | `daemon/process_control_unix.go`    |
| **Windows** | 创建 JobObject 并设置 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`，句柄关闭时自动清理所有子进程；再用 `taskkill /T /F` 做系统级兜底 | `daemon/process_control_windows.go` |



## 6. Runtime 文件发现机制

runtime 文件是 daemon 与 client 之间唯一的发现通道。

### 6.1 文件格式

`runtime.json` 是一个 JSON 文件，内容对应 `protocol.RuntimeInfo`：

```json
{
  "addr": "127.0.0.1:54321",
  "token": "1740000000000000000",
  "pid": 12345,
  "started_at": "2026-02-27T15:00:00Z"
}
```

### 6.2 写入原子性

`tcdruntime.Write` 采用 **先写 tmp 再 rename** 的方式保证原子性，避免 client 读到半写入的文件内容。

### 6.3 路径隔离

不同项目通过 `Project` 字段隔离到不同的 runtime 目录：

```
{os.TempDir()}/tcd/{project}/runtime.json
```

项目名中的特殊字符（`\`、`/`、`:`、空格）会被替换为 `_`，确保路径合法。

---

## 7. 安全边界

| 层面         | 策略                                                                             |
| ------------ | -------------------------------------------------------------------------------- |
| **网络**     | daemon 仅监听 `127.0.0.1`，不暴露到外部网络                                      |
| **认证**     | 每次请求要求 `X-Testcontainerd-Token` 头匹配，token 默认使用纳秒级时间戳自动生成 |
| **进程隔离** | runtime 文件按 Project 隔离，多项目并行时互不干扰                                |
| **资源回收** | lease TTL + 心跳 + reaper + runtime 清理多层兜底，确保即使异常退出也不会永久残留 |

---

## 8. 限制与约束

- **单机部署**：daemon 默认只监听 loopback，不支持跨机器共享容器。
- **Docker 依赖**：底层依赖 `testcontainers-go`，要求本机 Docker Engine 可用。
- **无持久化**：lease 存储在内存中，daemon 重启后所有 lease 丢失（设计如此，重启意味着环境重建）。
- **容器名全局唯一**：容器名与实例配置名绑定，同一 Docker host 上不能有手工创建的同名容器。
- **SUT 单实例**：daemon 同一时刻只管理一个 SUT 进程，不支持多 SUT 并行。
