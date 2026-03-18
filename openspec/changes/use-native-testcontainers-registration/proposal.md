## Why

当前 `testcontainerd` 的容器注册要求调用方使用 `container.InstanceConfig`，而这层仅是对 `testcontainers-go` 的轻量封装，反而收窄了调用方可用的配置能力。这种设计让 `testcontainerd` 持续承担额外维护成本（每新增一种容器都要补内部封装），但没有提供足够的附加价值，因此应将重心收敛到生命周期与租约管理。

## What Changes

- **BREAKING** 用原生 `testcontainers-go` 注册方式替代基于 `InstanceConfig` 的注册，调用方直接定义容器启动逻辑。
- **BREAKING** 移除内置容器类型封装与 spec-driver 映射，不再由框架组装默认端口/环境变量/URI。
- 增加“按注册项提供 SUT 环境变量”的契约：每个注册容器自行声明 SUT 启动所需环境变量。
- 增加 daemon 侧 SUT 环境变量聚合能力，保证聚合顺序确定，并对冲突进行检测。
- **BREAKING** 精简 acquire 契约：acquire 仅返回租约信息，不再返回容器连接资源载荷。
- 以功能领域重构能力边界：拆分为 integration API 接口、control-plane 接口、container 生命周期管理、SUT 管理四个领域并分别定义可验收行为。

## Capabilities

### New Capabilities
- `integration-api-interface`：定义暴露给调用方的集成接口能力，包括容器注册契约、启动入口与对外 API 语义，确保调用方以原生 `testcontainers-go` 方式接入。
- `control-plane-interface`：定义 client 与 daemon 之间的控制面协议能力，包括运行时发现、鉴权与 lease-only 的 acquire/heartbeat/release 行为。
- `container-lifecycle-management`：定义容器生命周期管理能力，包括并发启动、失败回滚、统一停止、空闲回收与 lease 驱动的运行策略。
- `sut-management`：定义 SUT 进程管理能力，包括按注册项环境变量注入、就绪探测、停止策略与空闲期回收行为。

### Modified Capabilities
- 无。

## Impact

- 受影响 API 面：`Registrar.Register(...)`、acquire 响应结构、SUT 启动输入模型。
- 受影响代码区域：`testcontainerd.go`、`container/registry.go`、`container/bundle.go`、`daemon/server.go`、`daemon/sut_manager.go`、`client/client.go`、`protocol/types.go`。
- 预期删除/重构：`container/options.go`、`container/spec/*`，以及描述封装容器类型的相关常量、文档与示例。
- 依赖策略：继续使用 `testcontainers-go` 作为运行时依赖，但不再在 `testcontainerd` 内镜像其容器模型。
