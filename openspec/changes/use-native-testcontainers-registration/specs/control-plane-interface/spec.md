## ADDED Requirements

### Requirement: Acquire is lease-only
control-plane 协议中的 acquire 响应 MUST 仅包含租约语义信息，不返回容器连接信息或资源端点载荷。

#### Scenario: Client acquires lease
- **WHEN** client 调用 acquire 成功
- **THEN** 响应仅包含租约标识与租约时间相关字段

### Requirement: Lease lifecycle endpoints remain complete
control-plane MUST 继续提供 acquire、heartbeat、release 的完整租约生命周期行为，并保证与 lease-only acquire 契约兼容。

#### Scenario: Heartbeat and release after lease-only acquire
- **WHEN** client 基于 lease-only 的 acquire 响应继续执行 heartbeat 与 release
- **THEN** daemon 正常续租与释放，不依赖任何容器连接字段

### Requirement: Runtime discovery and token auth are preserved
control-plane MUST 保持 runtime 发现与 token 鉴权机制，确保 client-daemon 连接路径与安全边界不因协议精简而退化。

#### Scenario: Client reconnects using runtime info
- **WHEN** client 通过 runtime 文件发现 daemon 并携带 token 发起请求
- **THEN** daemon 按现有鉴权规则处理请求并返回对应结果
