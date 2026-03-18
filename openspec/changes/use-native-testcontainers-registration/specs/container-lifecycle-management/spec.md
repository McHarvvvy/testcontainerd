## ADDED Requirements

### Requirement: Lifecycle management is container-model agnostic
容器生命周期管理 MUST 与具体容器类型解耦；框架不再依赖内置类型驱动来定义默认端口、默认环境变量或默认 URI 规则。

#### Scenario: Start heterogeneous containers without built-in type wrappers
- **WHEN** 注册项使用不同的原生 `testcontainers-go` 启动策略（含自定义 wait、网络、镜像参数）
- **THEN** 框架按统一生命周期流程托管，不要求容器属于预置类型集合

### Requirement: Start/rollback/stop semantics are preserved
框架 MUST 保持并发启动、失败回滚、统一停止的生命周期语义，且在任一注册项启动失败时回滚已创建容器。

#### Scenario: One container fails during startup
- **WHEN** 多容器并发启动过程中任一注册项返回启动失败
- **THEN** 框架终止已启动容器并使本次获取环境失败

### Requirement: Lease-driven idle reaping remains effective
容器资源回收 MUST 继续由 lease 状态与空闲 TTL 驱动，避免因注册模型变更导致空闲资源泄漏。

#### Scenario: No active lease beyond idle TTL
- **WHEN** 所有 lease 释放且空闲时间超过配置的 daemon idle TTL
- **THEN** 框架停止容器并关闭 daemon 运行实例
