## ADDED Requirements

### Requirement: 内部资源快照以连接字符串为核心
系统 MUST 在 daemon 与 SUT 启动链路中使用连接中心资源模型。每个资源项 SHALL 至少包含 `name` 与必填 `connection`，并可包含 `metadata` 扩展字段。框架 MUST 不承担从 host/port 推导标准连接字符串的职责。

#### Scenario: 生成最小资源项
- **WHEN** starter 成功返回资源结果
- **THEN** 系统 MUST 产出包含 name 与 connection 的内部资源项

#### Scenario: metadata 缺省
- **WHEN** starter 未返回 metadata
- **THEN** 系统 SHALL 允许资源项以空 metadata 继续流转

### Requirement: Acquire 响应不暴露资源
系统 SHALL 将资源快照限定在 daemon 内部和 SUT BootPlan 输入中使用。Acquire API MUST 仅返回 `lease_id` 与 `acquired_at`，不得包含任何资源端点字段。

#### Scenario: 客户端申请租约
- **WHEN** client 调用 Acquire 且基础设施与 SUT 已就绪
- **THEN** 响应 MUST 仅包含租约字段且不含资源信息

#### Scenario: SUT 启动使用内部资源快照
- **WHEN** daemon 进入 SUT 启动流程
- **THEN** 系统 MUST 将内部资源快照传入 SUT 启动输入

### Requirement: SUT 注入以连接字符串消费为主
系统 SHALL 允许 SUT BootPlan 通过资源快照中的 `connection` 直接构建环境变量，不依赖 host/port 等拆分字段。

#### Scenario: 生成 SUT 环境变量
- **WHEN** SUT BootPlan 读取资源快照
- **THEN** 启动输入 MUST 提供可直接消费的连接字符串用于注入

### Requirement: 资源读取操作保持快照一致性
系统 MUST 保证资源快照对外读取为只读语义，调用方修改读取结果不得影响 daemon 内部保存状态。

#### Scenario: 调用方修改读取副本
- **WHEN** 调用方修改读取到的 metadata 映射
- **THEN** daemon 内部保存的资源状态 MUST 不受影响
