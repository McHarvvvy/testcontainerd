## ADDED Requirements

### Requirement: Native container registration contract
`testcontainerd` MUST 提供面向调用方的注册契约，使调用方可以使用原生 `testcontainers-go` 启动逻辑注册容器，而不依赖 `InstanceConfig` 或内置容器类型映射。

#### Scenario: Register container with native startup logic
- **WHEN** 调用方在注册阶段提供容器名称与原生 `testcontainers-go` 启动函数
- **THEN** 框架接受该注册项并在后续生命周期中托管该容器

### Requirement: Registration item can export SUT env inputs
每个容器注册项 MUST 能独立声明其贡献给 SUT 的环境变量集合，且变量值格式不受框架限制（可为 host/port、URI 或其他字符串）。

### Requirement: Breaking migration boundary is explicit
对外集成接口 MUST 明确声明本次变更为破坏式升级，并给出从旧注册模型迁移到原生注册模型的接口边界。

#### Scenario: Caller uses removed InstanceConfig API
- **WHEN** 调用方继续使用旧的 `InstanceConfig` 风格注册接口
- **THEN** 框架在接口层给出明确不可用反馈，并指向原生注册契约
