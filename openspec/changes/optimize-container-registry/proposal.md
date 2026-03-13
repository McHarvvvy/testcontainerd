## Why

当前容器注册与运行模型将协议层（`protocol.ResourceEndpoint`）和框架内置容器类型（`TypeMySQL`/`spec`）耦合在一起，导致内部接口被 HTTP 回包形态反向约束，且扩展新容器时需要修改框架核心。现在需要将 testcontainerd 收敛为生命周期编排器，让“容器如何启动与解析资源”完全由用户和 testcontainers 定义，从而降低耦合并提升可扩展性。

## What Changes

- 重构注册模型：`Registrar.Register(...)` 从接收 `container.InstanceConfig` 调整为接收用户提供的启动定义（starter），并要求 starter 返回标准连接字符串。
- 下线类型驱动中心化模型：移除框架内置 `WithType`/`TypeMySQL` 与 `container/spec` 体系，不再由框架维护容器类型知识。
- 引入中立内部资源模型：以 `name` + 必填 `connection`（连接字符串）为核心，可选 `metadata` 扩展字段；不要求框架维护 host/port 到 URI 的推导逻辑。
- 协议收敛：`AcquireResp` 仅返回租约信息（`LeaseID`/`AcquiredAt`），不再返回资源端点。
- 调整 daemon/SUT 链路：`ensureInfraStarted` 与 `StartSUTInput.Resources` 继续使用内部资源快照，SUT 注入主要依赖连接字符串，不依赖 Acquire 回包。
- 同步示例与文档：更新 `README` 与 `examples` 到“用户提供启动定义”的新模型。

## Capabilities

### New Capabilities
- `starter-owned-connection-contract`: 支持用户通过 starter 注册容器启动行为，并由 starter 直接返回标准连接字符串。
- `connection-centric-resource-snapshot`: 建立仅在 daemon/SUT 内部流转的连接字符串中心资源快照模型，不暴露协议耦合字段。

### Modified Capabilities
- （无）当前 `openspec/specs/` 下无既有 capability 规范，本次以新增 capability 为主。

## Impact

- 受影响代码：`container/`（registry、bundle、drivers、runtime 模型）、`daemon/`（acquire/lifecycle/sut 输入）、`protocol/`（AcquireResp）、`client/`（Acquire 解析）以及 `examples/`、`README.md`。
- API 影响：Acquire 对外响应字段收缩（移除 resources）；属于协议兼容性变更，旧调用方若依赖资源回包需迁移。
- 注册契约影响：starter 需要返回非空连接字符串；框架不再提供 resolver 扩展点，也不负责从 host/port 推导标准 URI。
- 依赖影响：继续使用 `testcontainers-go` 与 Docker，不新增运行时基础依赖。
- 行为影响：资源信息改为 daemon 内部与 SUT BootPlan 专用，测试进程不再通过 Acquire 直接获取容器端点；SUT 注入路径以连接字符串为主。
