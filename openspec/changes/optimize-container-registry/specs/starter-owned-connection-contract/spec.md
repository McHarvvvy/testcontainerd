## ADDED Requirements

### Requirement: 注册输入必须包含可执行 starter
系统 MUST 支持通过注册接口声明容器实例，注册输入 SHALL 包含唯一名称与可执行 starter。框架 MUST 不再要求内置容器类型字段，也 MUST 不再依赖 type/spec 中央驱动。

#### Scenario: 注册缺少名称或 starter
- **WHEN** 用户注册缺少名称或 starter 的容器定义
- **THEN** 系统 MUST 返回带容器上下文的校验错误并拒绝注册

#### Scenario: 注册同名容器
- **WHEN** 用户重复注册相同名称的容器定义
- **THEN** 系统 MUST 返回重复名称错误并保持已有注册不变

### Requirement: starter 必须返回标准连接字符串
系统 SHALL 将“连接字符串如何生成”的职责完全交给 starter。starter 成功返回后，结果 MUST 包含非空标准连接字符串，框架 MUST 将其作为该容器的标准可消费连接入口。

#### Scenario: starter 返回非空连接字符串
- **WHEN** starter 完成容器启动并返回合法连接字符串
- **THEN** 系统 MUST 接受该结果并继续后续编排流程

#### Scenario: starter 返回空连接字符串
- **WHEN** starter 返回空连接字符串或仅空白字符
- **THEN** 系统 MUST 判定启动失败并返回校验错误

### Requirement: 编排框架保持生命周期一致性
系统 SHALL 在不理解容器类型语义的前提下，继续提供并发启动、失败回滚、全量启动后初始化和统一终止语义。

#### Scenario: 并发启动任一容器失败
- **WHEN** 已注册容器并发启动时任一 starter 失败
- **THEN** 系统 MUST 终止已创建容器并返回失败结果

#### Scenario: 全部容器启动后执行初始化
- **WHEN** 所有容器 starter 均成功完成
- **THEN** 系统 SHALL 在完整资源快照可用后执行 init 逻辑
