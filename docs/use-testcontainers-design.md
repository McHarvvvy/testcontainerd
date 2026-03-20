## Context

### 现状

testcontainerd 当前通过 `container.InstanceConfig` 封装容器配置，调用方必须遵循框架预设的字段模型（`Type`、`Image`、`Ports`、`Env`），再由框架内部的 `spec.Driver` 体系将配置翻译成 `testcontainers-go` 的 `ContainerRequest`，并自动推导 URI、端口等连接信息，最终将这些信息作为 `ResourceEndpoint` 集合打包在 `AcquireResp.Resources` 中返回给调用方。

SUT 启动阶段，`StartSUTInput.Resources` 将上述连接信息传入 `SUTBootPlan.GetCommand`，由调用方在 `GetCommand` 内解析 `ResourceEndpoint` 并手动拼接 `cmd.Env`，同时也在此处混入 `MSSIOT_ENV`、`MSSIOT_REGION` 等与容器无关的静态配置。

### 存在的问题

1. **二次封装冗余**：调用方已经熟悉 `testcontainers-go` 的原生 API，框架层的 `InstanceConfig` / `spec.Driver` 体系形成了不必要的中间抽象层，学习成本高。
2. **类型枚举硬编码**：`TypeMySQL / TypeRedis / TypeMongo / TypePulsar` 等枚举迫使框架跨版本维护多套 `spec.Driver`，每次新增中间件都需要修改框架核心代码。
3. **URI 构造不可控**：框架自动推导 URI 的逻辑无法统一覆盖所有连接字符串方言（DNS 解析、TLS、认证参数等），调用方往往需要绕开框架另行拼接。
4. **Acquire 返回连接信息造成职责混淆**：`AcquireResp.Resources` 将基础设施信息暴露给负责管理租约的 `client` 层，使得 client 层在概念上承担了"服务发现"职责，与其"租约管理"定位不符。
5. **SUT 环境变量注入路径混乱**：`GetCommand` 同时承担了"解析容器连接信息"和"注入 SUT 进程配置"两件事，缺乏清晰的层次：连接信息的来源（`ResourceEndpoint`）定义在框架内部，调用方需要理解框架内部数据结构才能正确使用；两类变量（连接类与静态类）混合在一处构造 `cmd.Env`，框架无法对各方来源做冲突检测。

### 修改动机

去掉框架对容器配置的二次封装，让调用方直接使用 `testcontainers-go` 原生 API 启动容器。SUT 环境变量的注入改为两段式、由框架统一合并：

- **容器侧**：各 `ContainerRegistration.Start()` 返回连接类 `SUTEnv`，框架在 daemon 内聚合，key 冲突立即报错。
- **SUT 侧**：`GetCommand` 在 `cmd.Env` 中声明 SUT 进程专属的静态配置（如 `MSSIOT_ENV`、`MSSIOT_REGION`），框架将其与容器 `SUTEnv` 合并，key 冲突立即报错，最终注入 cmd 启动 SUT。

调用方无需自行合并两类变量，各自只声明各自范围内的内容。

---

## Goals / Non-Goals

**Goals:**

- 将容器注册 API 从 `InstanceConfig`（框架定义字段）替换为 `ContainerRegistration`（调用方提供 `Start` 函数）。
- 废弃 `container/spec/` 和 `spec.Driver` 驱动体系，彻底解除框架对中间件类型的硬依赖。
- 容器连接信息由各 `Start()` 以 `SUTEnv` 的形式汇报，框架聚合后通过 `StartSUTInput.SUTEnv` 传入 `GetCommand`（供参考），同时由框架注入 SUT 进程。
- `GetCommand` 通过 `cmd.Env` 声明 SUT 进程专属的静态配置；框架将 `cmd.Env` 与容器 `SUTEnv` 在 daemon 层合并，key 冲突报错，然后统一注入 cmd。
- `AcquireResp` 只保留租约字段（`lease_id`、`acquired_at`），不再携带任何连接信息。
- 保持现有的双层租约、Reaper、幂等回滚、并发启动、逆序停止等核心机制不变。

**Non-Goals:**

- 不提供新的容器编排能力（健康检查、重启策略、网络拓扑等由调用方自行通过 `testcontainers-go` 实现）。
- 不实现容器连接信息在 client 侧的主动查询接口。
- 不修改 `Heartbeat`、`Release`、`Reaper`、`IdleTTL` 的行为语义。
- 不支持热更新注册项（Registry 冻结机制保留）。
- 不自动为调用方设置稳定容器名（这是最佳实践建议，由文档约束，不做框架强校验）。

---

## Interface Design

### 0. API 行为契约（`New` / `Run`）

为避免调用方感知内部实现细节，框架对外仅承诺以下可观察行为：

- `New(cfg, registerContainers)` 对配置做规范化：`Global.Project` 为空时默认填充为 `"default"`，`Global.RuntimePath` 为空时默认填充为系统默认 runtime 路径。
- 除 `Project` / `RuntimePath` 外，其他关键配置（如 daemon 监听地址、client 请求地址等）必须显式合法；不合法时 `New` 立即返回错误。
- `registerContainers` 不允许为 `nil`；返回空切片也视为非法输入，`New` 立即返回错误（容器注册列表不能为空）。
- 注册名冲突、`Name`/`Start` 为空等注册阶段错误在初始化阶段 fail-fast（`New` / `Bundle` 构建阶段），不进入运行态。
- `Run(m)` 仅在依赖容器与 SUT 均完成启动并就绪后才调用 `m.Run()`；任一前置阶段失败都不会执行 `m.Run()`，并返回 `1`。
- 若 `m.Run()` 被执行，`Run(m)` 原样透传其退出码（`0 -> 0`，`1 -> 1`）。
- 调用方无需区分 client/daemon 模式；模式选择与资源复用由框架在 `Run` 期间自动处理。

### 1. `ContainerRegistration`（`container` 包，替换 `InstanceConfig`）

```go
// StartedContainer 是调用方 Start 函数的返回值。
// Container 用于框架托管生命周期；SUTEnv 是该容器贡献给 SUT 进程的连接信息。
type StartedContainer struct {
    Container testcontainers.Container
    SUTEnv    map[string]string
}

// ContainerRegistration 表示单个容器注册项。
type ContainerRegistration struct {
    // Name 是注册项唯一标识，建议与容器 Name 保持一致，用于回滚、清理和日志。
    Name  string
    // Start 负责创建并启动容器，返回容器句柄和该注册项贡献的连接类 SUT 环境变量。
    Start func(ctx context.Context) (StartedContainer, error)
    // Init 在所有容器启动后执行（可选），可用于建库、写种子数据等初始化操作。
    Init  func(ctx context.Context) error
}
```

**行为约束：**

- `Name` 和 `Start` 均不得为空，否则 `Bundle` 构建时立即返回错误。
- `Init` 为 `nil` 时跳过，不报错。
- 同一 `Name` 不得重复，`Bundle` 构建时检测，重复即报错。
- 各注册项返回的 `SUTEnv` key 不得跨注册项重复；聚合阶段检测到冲突时 fail-fast，报告冲突双方的注册名，并触发全量回滚。

### 2. `RegisterContainersFunc`（`testcontainerd` 包顶层）

```go
// RegisterContainersFunc 是调用方在 daemon 模式下一次性声明所有容器注册项的函数。
// 框架在 daemon 启动前调用该函数，用返回的切片直接初始化 Bundle；
// 无需 Registrar 接口，无需 Registry 中间存储。
type RegisterContainersFunc func(ctx context.Context) ([]container.ContainerRegistration, error)
```

调用方在函数体内构造并返回完整的注册项切片，切片顺序即为容器的注册/启动顺序。
`RegisterContainersFunc` 的职责严格限定为**声明容器注册项**，不承载任何 SUT 进程相关的配置。
返回空切片属于非法输入，框架在初始化阶段直接报错（容器注册列表不能为空）。

### 3. `StartSUTInput` 与 `GetCommand` 语义（`daemon` 包）

```go
// StartSUTInput 定义被测服务启动输入（去掉 Resources，新增 SUTEnv）。
type StartSUTInput struct {
    Project     string
    RuntimePath string
    // SUTEnv 是由所有容器注册项 Start() 返回的连接类环境变量聚合而成，
    // 供 GetCommand 实现方参考（如动态决定使用哪个容器的连接信息）。
    // 框架会统一将其注入 SUT 进程，实现方无需自行写入 cmd.Env。
    SUTEnv map[string]string
}
```

**`GetCommand` 的职责与 `cmd.Env` 语义：**

- `GetCommand` 负责构建启动命令本身（路径、工作目录、参数）。
- 若需要向 SUT 进程注入**与容器无关的静态配置**（如 `MSSIOT_ENV`、`MSSIOT_REGION`），通过 `cmd.Env` 声明，框架将其与 `SUTEnv` 合并后注入，key 冲突即报错拒绝启动。
- `cmd.Env` 中**不应包含**已由 `SUTEnv` 覆盖的 key（容器连接类变量由框架统一注入，无需重复声明）。
- `cmd.Env` 为空时，框架仅注入 `SUTEnv`；若还需继承宿主机环境，调用方应在 `cmd.Env` 中显式包含 `os.Environ()`。
- `SUTBootPlan` 接口中的 `SetEnvEndpoint()` 方法随此次重构一并删除。

**框架侧 env 合并逻辑（`ensureStarted` 内部）：**

1. 取 `GetCommand` 返回的 `cmd.Env`（SUT 静态配置，可能含 `os.Environ()`）。
2. 将 `StartSUTInput.SUTEnv`（容器连接信息）逐 key 合并进 `cmd.Env`；若发现 key 重叠，报错并拒绝启动。
3. 用合并后的 env 替换 `cmd.Env` 后执行 `cmd.Start()`。

### 4. `AcquireResp`（`protocol` 包，最小化）

```go
// AcquireResp 表示租约申请响应（去掉 Resources）。
type AcquireResp struct {
    LeaseID    string    `json:"lease_id"`
    AcquiredAt time.Time `json:"acquired_at"`
}
```

`ResourceEndpoint` 类型与 `protocol` 包中的相关代码一并删除。

### 5. `Bundle.StartAll` 返回值变更（`container` 包）

```go
// StartAll 启动全部依赖容器，返回各注册项 SUTEnv 聚合后的连接信息。
func (b *Bundle) StartAll(ctx context.Context) (map[string]string, error)
```

`Bundle.Endpoints()` 方法删除；`Bundle.Containers()` 只返回注册名列表，去掉 `type/name` 格式。

### 6. `client.Acquire` 返回值变更

```go
// LeaseInfo 是 Acquire 的返回值，仅含租约信息。
type LeaseInfo struct {
    LeaseID    string
    AcquiredAt time.Time
}

func (c *Client) Acquire(ctx context.Context) (LeaseInfo, error)
```

`testcontainerd.go` 的 `runClientMode` 中，`SUT.SetEnvEndpoint()` 调用同步删除（该方法从 `SUTBootPlan` 接口移除）。

---

## Architecture

### 整体数据流

```
测试进程 (client mode)
    │
    ▼
client.Acquire()
    │  HTTP POST /acquire
    ▼
daemon.handleAcquire()
    ├─ ensureInfraStarted()
    │   ├─ bundle.StartAll()
    │   │   ├─ 并发调用各 ContainerRegistration.Start()
    │   │   │   └─ 调用方原生 testcontainers-go 逻辑，返回 (Container, SUTEnv)
    │   │   ├─ 聚合各容器 SUTEnv（key 冲突 → fail-fast + 全量回滚）
    │   │   └─ 顺序调用各 ContainerRegistration.Init()
    │   └─ 返回 containerSUTEnv（仅含容器连接信息）
    ├─ ensureSUTStarted(containerSUTEnv)
    │   └─ sutManager.ensureStarted()
    │       ├─ boot.GetCommand(ctx, StartSUTInput{SUTEnv: containerSUTEnv})
    │       │   └─ 返回 cmd（cmd.Env 含调用方声明的静态 SUT 配置）
    │       ├─ mergeEnv(cmd.Env, containerSUTEnv)（key 冲突 → 报错拒绝启动）
    │       └─ cmd.Start()（使用合并后的 env）
    └─ lease.Acquire() → LeaseID + AcquiredAt

client 侧收到 LeaseID，测试代码从 os.Getenv() 读取 SUT 地址等信息
（SUT 进程 env = 调用方静态配置 + 容器连接信息，由框架统一注入）
```

注：以上是框架内部执行路径。对调用方而言，仅需调用 `Run(m)` 并基于其返回值与环境可用性进行断言，不需要感知 client/daemon 的分支细节。

### 关键模块变更概览

| 模块                                | 变更类型    | 核心变化                                                                                                                              |
| ----------------------------------- | ----------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `container/options.go`              | 删除 + 重建 | 删除 `InstanceConfig`、`WithType` 等，新增 `ContainerRegistration`、`StartedContainer`                                                |
| `container/registry.go`             | 删除        | `Registry` 全局状态不再需要；校验逻辑上移至 `Bundle` 构建时                                                                           |
| `container/bundle.go`               | 重构        | `NewBundle` 接收 `[]ContainerRegistration`；`StartAll` 返回 `map[string]string`；新增 SUTEnv 聚合与冲突检测；删除 `Endpoints()`       |
| `container/drivers.go`              | 删除        | 框架不再构建 `ContainerRequest`                                                                                                       |
| `container/spec/`                   | 删除        | 彻底移除 spec.Driver 体系                                                                                                             |
| `protocol/types.go`                 | 修改        | 删除 `ResourceEndpoint`、`AcquireResp.Resources`                                                                                      |
| `daemon/sut_manager.go`             | 修改        | `StartSUTInput` 去掉 `Resources`，加入 `SUTEnv`；`ensureStarted` 新增 env 合并与冲突检测                                              |
| `daemon/server.go` / `lifecycle.go` | 修改        | `New` 接收 `[]ContainerRegistration`（替代 `*Registry`）；`ensureInfraStarted` 返回 `SUTEnv`；`handleAcquire` 响应精简                |
| `client/client.go`                  | 修改        | `Acquire` 返回 `LeaseInfo`（无 Resources）                                                                                            |
| `testcontainerd.go`                 | 修改        | 删除 `Registrar` 接口、`RegisterContainersHook`、`registryRegistrar`；新增 `RegisterContainersFunc`；删除 `SUT.SetEnvEndpoint()` 调用 |
| `constant/container.go`             | 清理        | 删除仅服务于 spec 的类型常量                                                                                                          |

---

## Key Technical Decision

### KTD-1：以"调用方直接提供 Start 函数"替代"框架配置字段翻译"

**决策**：`ContainerRegistration.Start` 是类型为 `func(ctx context.Context) (StartedContainer, error)` 的函数，调用方在函数体内使用 `testcontainers-go` 原生 API，框架只调用该函数并托管返回的 `testcontainers.Container`。

**原因**：消除 `InstanceConfig` → `spec.Driver` → `ContainerRequest` 这条翻译链，使框架不再需要跟进 `testcontainers-go` 的 API 演进；调用方拥有完整控制权，可使用任意 module（`testcontainers/mysql`、自定义镜像等）而无需框架适配。

**代码影响**：`container/drivers.go`、`container/spec/` 全量删除；`bundle.startContainer` 替换为直接调用 `reg.Start(ctx)`。

---

### KTD-2：SUT 环境变量分两段声明，由框架统一合并注入

**决策**：SUT 进程最终收到的环境变量由两部分构成，分别通过两条独立路径声明，框架在 `ensureStarted` 内串行合并，任一 key 冲突立即报错拒绝启动：

| 来源             | 声明路径                                                          | 内容                                 |
| ---------------- | ----------------------------------------------------------------- | ------------------------------------ |
| 容器连接信息     | 各 `ContainerRegistration.Start()` 返回 `StartedContainer.SUTEnv` | MySQL DSN、Redis addr、Pulsar URL 等 |
| SUT 进程静态配置 | `SUTBootPlan.GetCommand()` 设置 `cmd.Env`                         | `MSSIOT_ENV`、`MSSIOT_REGION` 等     |

合并顺序：先聚合各容器 `SUTEnv`（key 冲突触发全量回滚），再将容器 `SUTEnv` 逐 key 注入 `cmd.Env`（key 冲突报错拒绝启动），更新后的 `cmd.Env` 用于 `cmd.Start()`。

**原因**：这一分层设计保持了职责边界——`Registrar` 与容器有关，`GetCommand` 与 SUT 进程有关，两条路径互不越界；框架在统一合并点做冲突检测，语义清晰，错误信息可明确指向两类来源之一。

**代码影响**：`sut_manager.ensureStarted` 在 `GetCommand` 之后新增合并步骤，替换原有 `if len(cmd.Env) == 0 { cmd.Env = os.Environ() }` 逻辑。

---

### KTD-3：AcquireResp 不携带任何连接信息

**决策**：`AcquireResp` 仅含 `lease_id` 和 `acquired_at`，`ResourceEndpoint` 和 `AcquireResp.Resources` 一并删除。

**原因**：client 层的职责是"租约管理"，与"基础设施服务发现"应当严格分离。连接信息由 daemon 在启动 SUT 时注入进子进程环境，测试代码通过 `os.Getenv()` 读取，无需 client 传递，消除了一条不必要的信息传播路径。

**影响**：`testcontainerd.go` 中的 `SUT.SetEnvEndpoint()` 调用和 `SUTBootPlan` 中的该方法随之删除。

---

### KTD-4：Init 函数签名简化，不再传入 RuntimeView

**决策**：`ContainerRegistration.Init` 签名为 `func(ctx context.Context) error`，不再像旧版 `InitInput` 那样传入 `Self` 和 `RuntimeView`。

**原因**：调用方在 `Start` 闭包内已经持有容器引用，`Init` 通过闭包捕获即可访问任意上下文，无需框架通过参数传递。去掉 `RuntimeView` 后框架也不必维护跨注册项的运行时视图结构，代码复杂度降低。

---

### KTD-5：孤儿清理依赖调用方设置稳定容器名

**决策**：框架在 `daemon/cleanup.go` 中保留按注册名清理孤儿容器的逻辑，但不强制校验调用方是否设置了固定名称。文档层面要求：在 `Start` 函数内通过 `ContainerRequest.Name` 设置与注册项 `Name` 一致的容器名。

**原因**：强校验（读取容器名再比对）需要在 `Start` 返回后额外调用 Docker API，增加启动耗时和 Docker 依赖复杂度。改为文档约束是实用主义选择：大多数场景调用方会遵循，少数不遵循的场景孤儿清理不生效也可接受（容器会在测试结束时由 `Terminate` 正常清理）。

---

### KTD-6：以"调用方一次性返回注册项切片"替代"Registrar 接口 + Registry 全局状态"

**决策**：删除 `Registrar` 接口、`RegisterContainersHook`、`registryRegistrar` 适配器和 `container.Registry`，改为单一函数类型 `RegisterContainersFunc func(ctx context.Context) ([]container.ContainerRegistration, error)`。调用方在函数体内构造完整切片，框架在 daemon 启动前调用一次，结果直接用于初始化 `Bundle`。

**原因**：

1. **语义对齐**：调用方的实际意图是"声明我需要这些容器"，返回切片的函数直接表达这一意图；而 `Register(single)` 是命令式逐个调用，迫使调用方写多次调用和逐一错误处理，语义错位。
2. **消除全局可变状态**：`Registry` 是 hook 调用与 daemon 启动之间的中间状态容器，需要依赖 `Freeze()` 的调用时序来保证一致性——这是一种隐式的全局变量模式。改为函数返回值后，配置的生命周期完全由函数调用控制，不存在竞态或时序依赖。
3. **校验提前**：`Name`/`Start` 非空校验和名称唯一性校验可在 `NewBundle` 构建时统一完成，错误在 daemon 初始化阶段即暴露，而不是等到 `StartAll` 运行时。
4. **语义收敛**：将“hook 存在性、注册列表非空、注册项合法性”统一前移到初始化阶段，可避免 `Run` 期间才发现配置级错误，调用方故障定位更直接。

**代码影响**：`container/registry.go` 全量删除；`container.NewBundle` 签名改为 `NewBundle(regs []ContainerRegistration) (*Bundle, error)`；`daemon.New` 签名改为 `New(cfg Config, regs []ContainerRegistration) (*Daemon, error)`；`testcontainerd.go` 删除 `Registrar`、`RegisterContainersHook`、`registryRegistrar`，新增 `RegisterContainersFunc`。

---

## Risks / Trade-offs

| 风险                                                                    | 严重度 | 防护措施                                                                              |
| ----------------------------------------------------------------------- | ------ | ------------------------------------------------------------------------------------- |
| 调用方 `Start` 实现质量参差，框架无法感知内部配置错误                   | 中     | 注册时校验 `Name`/`Start` 非空；`Start` 返回错误时单独报告注册名，触发全量回滚        |
| 容器间 `SUTEnv` key 冲突                                                | 中     | 聚合时 fail-fast，错误信息包含冲突的两个注册名和 key，定位成本低                      |
| `cmd.Env` 与 `SUTEnv` key 冲突                                          | 中     | 合并时 fail-fast，错误信息明确指出冲突的 key 及其来源（容器注册名 vs `cmd.Env`）      |
| 删除 `SetEnvEndpoint` 后，测试代码（非 SUT 子进程）无法直接获取连接信息 | 低     | 该场景属于 Non-Goal；测试进程本身读不到 SUT 子进程的 env，这本就是隔离设计            |
| 调用方不设置固定容器名，孤儿清理失效                                    | 低     | 文档约束 + 示例强制设名；Terminate 路径保证正常退出情况下不留孤儿                     |
| `testcontainers-go` 版本之间行为差异由调用方自行承担                    | 低     | 这是权衡收益的一部分，框架不再需要跨版本维护 spec driver                              |
| 破坏式 API 变更，调用方迁移成本高                                       | 高     | 提供迁移指南和示例（`examples/` 重写）；新 API 在表达力上优于旧 API，迁移可获得净收益 |
