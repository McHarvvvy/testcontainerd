## ADDED Requirements

### Requirement: SUT env is aggregated from registration items
SUT 管理流程 MUST 从每个容器注册项收集环境变量并在 daemon 侧聚合后注入 SUT 进程启动环境。

#### Scenario: Multiple registrations contribute SUT env
- **WHEN** 多个注册项分别导出不同的 SUT 环境变量
- **THEN** daemon 聚合这些变量并在启动 SUT 前注入进程环境

### Requirement: Env conflict handling is deterministic
当不同注册项导出相同环境变量键时，框架 MUST 按确定性规则处理，且冲突结果必须可预测并可诊断。

#### Scenario: Two registrations export same env key
- **WHEN** 两个注册项同时导出相同键名但不同值
- **THEN** 框架按既定冲突策略返回可诊断失败，且不进入不确定覆盖行为

### Requirement: SUT readiness and shutdown guarantees remain
SUT 管理 MUST 保持就绪探测、优雅停止与空闲期回收语义，且不依赖 acquire 响应中的容器连接载荷。

#### Scenario: Start SUT after lease-only acquire
- **WHEN** acquire 仅返回租约信息
- **THEN** SUT 仍可依赖聚合后的环境变量完成启动探测并进入就绪状态
