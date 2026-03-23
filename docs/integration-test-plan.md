# testcontainerd 集成测试计划

> **被测方法**：`New(cfg, registerContainers)` 和 `(*TestContainerd).Run(m *testing.M)`
> **测试类型**：集成测试（不 mock 框架内部，允许 fake 外部依赖如 `*testing.M`）
> **测试边界**：以外部可观察状态为断言对象——`Run()` 返回值、runtime 文件存在性、daemon HTTP 可达性、租约生命周期
> **基础设施**：所有容器场景使用真实 Docker Engine + `testcontainers-go`；**不使用任何 stub/mock**
> 从程序运行机制上说不用区分 client 模式和 daemon 模式，这个是调用 Run 函数之后程序自动处理的，调用者视角不需要知道这些细节

### 执行策略

- **串行执行**：所有测试用例串行运行，不使用 `t.Parallel()`。避免用例之间因共享 Docker daemon、端口、runtime 文件等产生干扰。
- **主动环境清理**：每个涉及 `Run()` 的用例（TC-05/06/08/09/10/12/13/14/15/18）在所有断言执行完毕后，必须主动清理环境残留产物——即使框架已自行回收，也通过 `t.Cleanup` 或 `defer` 兜底：强制终止 daemon 进程（如仍存活）、通过 Docker API 销毁已注册的容器（如仍存在）、删除 runtime.json（如仍存在）。这是测试隔离的保证，不是被测行为的断言。
- **容器清理断言依据**：框架在回滚和正常关闭路径中均显式调用 `container.Terminate()`（`bundle.rollback()` → `stopAllContainers()` → `ctr.Terminate(ctx)`），因此验证容器被清理是对框架自身行为的断言，不是测试 testcontainers reaper 机制。
- **仅 New 阶段的校验用例**（TC-01/02/03/04/07/11/17）不启动容器和 daemon，无需清理步骤。

---

## Feature: 配置规范化（New）

### TC-01 项目名称和RuntimePath为空时自动填充默认值

**说明**：调用方传入空 Project 和 RuntimePath 时，框架应静默补全为默认值，且内部使用的 runtime 路径与 `tcdruntime.DefaultRuntimePath("default")` 推导结果一致。

```gherkin
Given 构造 Config，其中 Global.Project = "" 且 Global.RuntimePath = ""
And   准备一个合法的 RegisterContainersFunc
When  调用 New(cfg, registerContainers)
Then  New 返回成功
And   框架内部使用的 runtime 路径等于 tcdruntime.DefaultRuntimePath("default") 的推导结果
```

### TC-02 除了 Project 和 RuntimePath，其他配置项不允许为空

**说明**：除了 Project 和 RuntimePath 可以依赖默认值，Config 中的其他关键字段如果不合法，在初始化阶段就应该被拒绝。

```gherkin
Given 构造多组 Config 测试用例，每组中包含一个非 Project 和 RuntimePath 的关键字段为空或非法：
        - Daemon.Addr 为空
        - Client.HTTPTimeout 为 0
        - Daemon.IdleTTL 为 0
And   准备一个合法的 RegisterContainersFunc
When  分别使用这些 Config 调用 New(cfg, registerContainers)
Then  New 函数立即返回 error
And   错误信息中明确指出无效的字段参数
```

### TC-03 RegisterContainersFunc 不允许为 nil

**说明**：调用方必须显式提供 `RegisterContainersFunc`，不允许传入 nil。

```gherkin
Given 构造合法 Config
When  调用 New(cfg, nil)
Then  New 函数立即返回 error
And   错误信息中明确指出 registerContainers 不能为空
```

---

## Feature: 注册阶段校验（New）

### TC-04 RegisterContainersFunc 不允许返回空列表

**说明**：如果 `RegisterContainersFunc` 返回空切片，说明没有任何容器需要框架接管，这属于非法/无意义的配置。框架应当在初始化阶段直接报错。

```gherkin
Given 构造合法 Config
And   提供一个 RegisterContainersFunc，它返回空切片 []ContainerRegistration{}
When  调用 New(cfg, registerContainers)
Then  New 返回 error，错误信息中明确指出"注册容器列表不能为空"
```

### TC-07 容器注册名重复

**说明**：由于配置中的重名导致注册逻辑非法时，`New()` 应当在初始化阶段报错。

```gherkin
Given 构造合法 Config
And   RegisterContainersFunc 返回两个 Name 均为 "redis" 的 ContainerRegistration
When  调用 New(cfg, registerContainers)
Then  New 返回 error，指明容器命名冲突
```

### TC-11 注册项 Name 为空或 Start 为 nil

**说明**：每个 `ContainerRegistration` 的 `Name` 和 `Start` 均为必填字段，缺失时应在初始化阶段 fail-fast。

```gherkin
Given 构造合法 Config
And   RegisterContainersFunc 返回的注册项中：
        子用例 A：某个注册项 Name 为空字符串，Start 合法
        子用例 B：某个注册项 Name 合法，Start 为 nil
When  调用 New(cfg, registerContainers)
Then  New 返回 error，明确指出不合法的字段（Name 为空 / Start 为 nil）
```

### TC-17 RegisterContainersFunc 自身返回 error

**说明**：`RegisterContainersFunc` 在构造注册项时可能遇到自身的错误（如配置读取失败），框架应透传该错误。

```gherkin
Given 构造合法 Config
And   RegisterContainersFunc 返回 (nil, fmt.Errorf("config load failed"))
When  调用 New(cfg, registerContainers)
Then  New 返回 error，且错误链中包含 "config load failed"
```

---

## Feature: 核心测试执行流（Run）

> **说明**：以下场景全部站在测试框架调用方的视角，验证其调用 `TestContainerd.Run(m)` 能否提供预期的环境保障，不再区分底层的 client/daemon 实现。
> **容器可观察性说明**：新设计中 `AcquireResp` 仅含租约信息，测试进程不再通过框架 API 获取容器连接信息。需要验证容器可用性时，通过 Docker API 按注册项 `Name` inspect 容器获取映射端口，再验证连通性。这要求注册项的 `Start` 函数中设置 `ContainerRequest.Name` 与注册项 `Name` 一致。

### TC-05 成功接管测试并透传退出码

**说明**：调用 `Run(m)` 时，框架应确保所注册的真实容器已启动且可用。最后将 `m` 的测试退出码如实向上透传。

```gherkin
Given 构造合法 Config
And   RegisterContainersFunc 注册一个真实 Redis 容器，
      注册项 Name 为 "redis-tc05"，Start 中设置 ContainerRequest.Name 与之一致
# defer/t.Cleanup 注册兜底清理：强制停止 daemon（如存活）、销毁容器 "redis-tc05"（如存在）、删除 runtime.json（如存在）
When  调用 New() 后执行 Run(fakeM)
Then  Run 阻塞执行
And   fakeM.Run() 内部通过 Docker API 按容器名 "redis-tc05" inspect 获取映射端口，
      并验证 Redis PING 连通性
And   fakeM.Run() 返回 0 时 Run 返回 0；fakeM.Run() 返回 1 时 Run 返回 1
```

### TC-06 连续执行测试框架复用底层基础设施

**说明**：调用方在多个连续的实例中多次调用框架，框架会自动复用已启动好的底层资源容器群，防止重复消耗机器性能。

```gherkin
Given 构造合法 Config（IdleTTL 设置足够长以覆盖两次 Run 间隔）
And   RegisterContainersFunc 注册一个真实 Redis 容器，
      注册项 Name 为 "redis-tc06"，Start 中设置 ContainerRequest.Name 与之一致
# defer/t.Cleanup 注册兜底清理：强制停止 daemon（如存活）、销毁容器 "redis-tc06"（如存在）、删除 runtime.json（如存在）
When  连续执行两次完全独立的 testcontainerd 实例的 Run(fakeM)
Then  两次 Run 调用都成功返回 0
And   两次 fakeM.Run() 内部通过 Docker API inspect 获取的容器 ID 相同，
      证明复用了同一个底层 Redis 容器
```

---

## Feature: 启动异常与回滚（Run）

### TC-08 容器部分启动失败触发全量回滚

**说明**：在测试运行前的容器启动阶段如果有任意一个发生错误，框架必须回滚所有已正常拉起的容器。

```gherkin
Given 构造合法 Config
And   RegisterContainersFunc 返回两个 ContainerRegistration：
        注册项 A：Start 正常启动真实 Redis 容器（Name 为 "redis-good-tc08"）
        注册项 B：Start 使用不存在的镜像 "redis:non-existent-tag-for-testcontainerd"，
                  导致 testcontainers-go 报错
# defer/t.Cleanup 注册兜底清理：强制停止 daemon（如存活）、销毁容器 "redis-good-tc08"（如存在）、删除 runtime.json（如存在）
When  调用 Run(fakeM)
Then  Run 返回 1
And   fakeM.Run() 不会被调用
And   通过 Docker API 验证容器 "redis-good-tc08" 已不存在（框架显式回滚的结果）
```

### TC-12 Init 函数失败触发全量回滚

**说明**：所有容器启动成功后，如果某个注册项的 `Init` 函数返回错误，框架必须回滚所有已启动的容器，确保下次 Acquire 从干净环境开始。

```gherkin
Given 构造合法 Config
And   RegisterContainersFunc 返回两个 ContainerRegistration：
        注册项 A：Start 正常启动真实 Redis 容器（Name 为 "redis-init-tc12"），Init 为 nil
        注册项 B：Start 正常启动另一个真实 Redis 容器（Name 为 "redis-init-fail-tc12"），
                  Init 函数返回 fmt.Errorf("init failed: seed data error")
# defer/t.Cleanup 注册兜底清理：强制停止 daemon（如存活）、销毁以上两个容器（如存在）、删除 runtime.json（如存在）
When  调用 Run(fakeM)
Then  Run 返回 1
And   fakeM.Run() 不会被调用
And   通过 Docker API 验证两个容器均已不存在（框架显式全量回滚的结果）
```

---

## Feature: SUTEnv 机制（Run）

### TC-09 多容器 SUTEnv key 跨注册项冲突

**说明**：各注册项 `Start()` 返回的 `StartedContainer.SUTEnv` 由框架在 daemon 内聚合。如果不同注册项的 SUTEnv 包含相同的 key，框架应 fail-fast 并全量回滚。

```gherkin
Given 构造合法 Config
And   RegisterContainersFunc 返回两个 ContainerRegistration：
        注册项 A：Start 正常启动真实容器，返回 SUTEnv = {"DB_ADDR": "containerA:3306"}
        注册项 B：Start 正常启动真实容器，返回 SUTEnv = {"DB_ADDR": "containerB:3306"}（key 冲突）
# defer/t.Cleanup 注册兜底清理：强制停止 daemon（如存活）、销毁以上两个容器（如存在）、删除 runtime.json（如存在）
When  调用 Run(fakeM)
Then  Run 返回 1
And   fakeM.Run() 不被调用
And   错误信息包含冲突的 key "DB_ADDR" 和涉及的两个注册项名称
And   通过 Docker API 验证两个容器均已不存在（框架显式全量回滚的结果）
```

### TC-13 cmd.Env 与容器 SUTEnv key 冲突

**说明**：框架在启动 SUT 时会合并 `GetCommand` 返回的 `cmd.Env`（SUT 静态配置）与容器聚合的 `SUTEnv`（连接信息）。如果两者存在相同的 key，框架应拒绝启动并报错。这与 TC-09（容器间冲突）是不同的合并点和代码路径。

```gherkin
Given 构造合法 Config，启用 SUT 托管
And   RegisterContainersFunc 注册一个真实容器，Start 返回 SUTEnv = {"APP_PORT": "3306"}
And   SUTBootPlan.GetCommand 在 cmd.Env 中声明了 "APP_PORT=8080"（与容器 SUTEnv key 冲突）
# defer/t.Cleanup 注册兜底清理：强制停止 daemon 和 SUT 进程（如存活）、销毁容器（如存在）、删除 runtime.json（如存在）
When  调用 Run(fakeM)
Then  Run 返回 1
And   fakeM.Run() 不被调用
And   错误信息明确指出 "APP_PORT" 在容器 SUTEnv 与 cmd.Env 之间发生冲突
```

### TC-18 SUTEnv 成功注入 SUT 进程环境变量

**说明**：容器 `Start()` 返回的 `SUTEnv` 在正常流程中应被框架注入到 SUT 子进程的环境变量中，SUT 进程可通过 `os.Getenv` 读取这些值。

```gherkin
Given 构造合法 Config，启用 SUT 托管
And   RegisterContainersFunc 注册一个真实 Redis 容器，
      Start 返回 SUTEnv = {"TEST_REDIS_ADDR": "<实际映射地址>"}
And   SUTBootPlan.GetCommand 返回一个 helper 进程，
      该进程启动后将自身 os.Getenv("TEST_REDIS_ADDR") 的值写入一个临时文件，然后监听探测端口
# defer/t.Cleanup 注册兜底清理：强制停止 daemon 和 SUT 进程（如存活）、销毁容器（如存在）、删除 runtime.json（如存在）
When  调用 Run(fakeM)
Then  Run 返回 0
And   fakeM.Run() 内部读取临时文件，验证其内容等于容器实际映射的 Redis 地址，
      证明框架确实将容器 SUTEnv 注入了 SUT 进程的环境变量
```

---

## Feature: SUT 就绪探测（Run）

### TC-10 SUT 就绪探测保证 fakeM.Run() 执行时端口已就绪

**说明**：SUT 开启服务探活时，框架承诺给到 `m` 实例的环境不仅拉起成功，更是网络服务完成就绪的。无论 SUT 启动延迟多久，进入 `m.Run()` 时目标探测端口必定已就绪可连。

```gherkin
Given 构造包含启用真实 SUT 探活计划（探测某一端口）的合法 Config
And   真实被测 SUT 进程模拟启动延迟：延迟耗时 300ms 后才绑定目标监听端口
# defer/t.Cleanup 注册兜底清理：强制停止 daemon 和 SUT 进程（如存活）、销毁容器（如存在）、删除 runtime.json（如存在）
When  调用 Run(fakeM)
Then  fakeM.Run() 内部对 SUT 监听端口发起 TCP 连接，连接成功
And   Run 正常返回 0
```

---

## Feature: 生命周期回收

### TC-15 Daemon 空闲退出

**说明**：当所有 lease 释放且无新请求时，daemon 应在 `IdleTTL` 超时后自动退出并清理 runtime 文件。内部 reaper 每 2s 轮询一次（hardcoded），因此将 IdleTTL 设为 1s，确保首次 reaper 检查时 idle 时间已超过阈值。框架自动退出后通过断言验证清理结果；完成后兜底清理处理任何残留。

```gherkin
Given 构造合法 Config，Daemon.IdleTTL 设置为 1s
And   RegisterContainersFunc 注册一个真实 Redis 容器
# defer/t.Cleanup 注册兜底清理：强制停止 daemon（如存活）、销毁容器（如存在）、删除 runtime.json（如存在）
When  调用 Run(fakeM)，fakeM.Run() 立即返回 0（lease 随即释放）
Then  Run 返回 0
And   在最多 10s 的容忍窗口内轮询断言（此处是核心行为验证）：
      - runtime.json 文件已被删除（框架 shutdownServer 的结果）
      - daemon 进程已退出（PID 不再存活）
      - 容器已不存在（框架显式 Terminate 的结果）
```

---

## Feature: 并发安全

### TC-14 多 client 并发 Acquire 复用同一容器

**说明**：`go test ./...` 场景下多个测试进程并发请求 daemon，所有请求应成功获取 lease 并共享同一组容器。本用例内部使用并发操作验证框架的并发安全性。

```gherkin
Given 构造合法 Config
And   RegisterContainersFunc 注册一个真实 Redis 容器（Name 为 "redis-tc14"）
# defer/t.Cleanup 注册兜底清理：强制停止 daemon（如存活）、销毁容器 "redis-tc14"（如存在）、删除 runtime.json（如存在）
When  启动一个 daemon 实例
And   3 个独立 client 并发执行 Acquire
Then  3 个 Acquire 全部成功，返回不同的 LeaseID
And   通过 Docker API inspect 容器 "redis-tc14"，验证只有一个 Redis 容器在运行
And   所有 client 执行 Release
```

---

- [x] 所有断言均指向外部可观察状态（返回值、runtime 文件清理状态、TCP 端口实际可用性、Docker API inspect 结果）
- [x] 移除对于底层 testcontainers reaper 重复造轮子的黑盒清理测试，只验证本框架职责内的 daemon 退出与业务服务可用性
- [x] 涉及真实容器的用例均依赖真实 testcontainers-go 启动容器，不使用 mock/stub
- [x] 失败用例的 Then 均可指向具体行为假设
- [x] 覆盖了输入边界、异常恢复、部分成功时的回滚
- [x] 覆盖了 SUTEnv 的正向注入与两条冲突检测路径（容器间、cmd.Env 与 SUTEnv）
- [x] 覆盖了生命周期回收和并发安全场景
- [x] 所有用例串行执行，不使用 t.Parallel()
- [x] 所有涉及 Run() 的用例通过 defer/t.Cleanup 注册兜底清理，断言完成后主动清理残留产物
- [x] 容器清理断言仅验证框架显式 Terminate 行为（bundle.rollback/StopAll），不验证 testcontainers reaper 机制
