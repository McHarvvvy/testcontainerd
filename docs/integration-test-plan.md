# testcontainerd 集成测试计划

> **被测方法**：`New(cfg, registerHook)` 和 `(*TestContainerd).Run(m *testing.M)`  
> **测试类型**：集成测试（不 mock 框架内部，允许 fake 外部依赖如 `*testing.M`）  
> **测试边界**：以外部可观察状态为断言对象——`Run()` 返回值、runtime 文件存在性、daemon HTTP 可达性、租约生命周期  
> **基础设施**：所有容器场景使用真实 Docker Engine + `testcontainers-go`；**不使用任何 stub/mock**
> 从程序运行机制上说不用区分 client 模式和 daemon 模式，这个是调用 Run 函数之后程序自动处理的，调用者视角不需要知道这些细节
---

## Feature: 配置规范化（New）

### TC-01 项目名称和RuntimePath为空时自动填充默认值

**说明**：调用方传入空 Project 和 RuntimePath 时，框架应静默补全为默认值，且可以在默认路径下找到 runtime.json 文件。

```gherkin
Given 构造 Config，其中 Global.Project = "" 且 Global.RuntimePath = ""
And   准备一个返回空列表的 fakeHook
When  调用 New(cfg, fakeHook)
Then  返回的 TestContainerd 实例的工程名为 "default"
And   可以在系统默认的 runtime 路径下找到对应的 runtime.json 文件路径
```

### TC-02 除了 Project 和 RuntimePath，其他配置项不允许为空

**说明**：除了 Project 和 RuntimePath 可以依赖默认值，Config 中的其他关键字段如果不合法，在初始化阶段就应该被拒绝。

```gherkin
Given 构造多组 Config 测试用例，每组中包含一个非 Project 和 RuntimePath 的关键字段为空（如 Daemon 监听地址、Client 请求地址等）
And   准备一个返回空列表的 fakeHook
When  分别使用这些 Config 调用 New(cfg, fakeHook)
Then  New 函数立即返回 error
And   错误信息中明确指出无效的字段参数（例如包含 "parameter invalid" 或类似描述）
```

### TC-03 registerHook 不允许为 nil

**说明**：调用方必须显式提供 registerHook，即使不需要容器也应传入返回空列表的函数，不允许传入 nil。

```gherkin
Given 构造合法 Config
When  调用 New(cfg, nil)
Then  New 函数立即返回 error
And   错误信息中明确指出 registerHook 不能为空
```

---

## Feature: 配置规范化与初始化异常（New / Run）

### TC-04 registerHook 不允许返回空列表

**说明**：如果 registerHook 返回空切片，说明没有任何容器需要框架接管，这属于非法/无意义的配置。框架应当在评估该 hook 时（New 阶段或 Run 启动 daemon 阶段）直接报错退回。

```gherkin
Given 构造合法 Config
And   提供一个 registerHook，它返回空列表 `[]ContainerRegistration{}`
When  调用 New()
Then  报错，错误信息中明确指出“注册容器列表不能为空”
```

## Feature: 核心测试执行流（Run）

> **说明**：以下场景全部站在测试框架调用方的视角，验证其调用 `TestContainerd.Run(m)` 能否提供预期的环境保障，不再区分底层的 client/daemon 实现。

### TC-05 成功接管测试并透传退出码机制

**说明**：调用 `Run(m)` 时，框架应确保所注册的真实容器以及 SUT 都按照配置启动，而在此时执行 `m.Run()` 时这些资源必须已经切实可用。最后将 `m` 的测试退出码如实向上透传。

```gherkin
Given 构造合法 Config，不强制指定任何底层运行模式
And   registerHook 注册一个依赖真实 Redis 的容器
When  调用 New() 后执行 Run(fakeM)
Then  Run 阻塞执行
And   fakeM.Run() 在内部直接验证 docker 中有那个名字的 redis 容器，redis连接可用。
And   fakeM.Run() 返回 0，则 Run 返回 0；fakeM.Run() 返回 1，则 Run 返回 1
And   测试执行完成后，本地 daemon 根据生命周期规则正常退出并清理自身 runtime 占用
```

### TC-06 连续执行测试框架复用底层基础设施

**说明**：调用方在多个连续的实例（并行或串行测试包）中多次调用框架，框架会自动复用已启动好的底层资源容器群，防止重复消耗机器性能。

```gherkin
Given 构造合法 Config
And   registerHook 注册一个依赖真实 Redis 的容器
And   fakeM.Run() 返回 0
When  连续（或并发）执行两次完全独立的 testcontainerd 分配实例的 Run(fakeM)
Then  两次 Run 调用都成功返回 0
And   第一次与第二次 fakeM.Run() 内部验证的均为同一个底层真实 Redis 容器的连通性
And   全部运行调用和租约生命周期结束后，daemon 按规则正常超时退出或被安全销毁
```

### TC-07 容器注册与启动时检测到非法状态

**说明**：由于配置中的重名等原因导致环境注册逻辑非法时，`Run()` 应当提早报错返回 1，且不能触发执行内部的 `m.Run()` 业务。

```gherkin
Given 构造合法 Config
And   registerHook 返回了两个同名为 "redis" 的容器项
When  调用 New()
Then  New（） 报错，容器命名冲突
```

### TC-08 进程中容器部分启动失败并触发全局回滚

**说明**：在测试运行前的容器启动阶段如果有任意一个发生错误，框架必须回滚所有在这之前已正常拉起的成功容器隔离环境。

```gherkin
Given 构造合法 Config
And   registerHook 返回两个容器配置：
        容器 A 正常启动真实的 Redis 容器
        容器 B 的 Start 函数模拟因故启动失败直接返回 error
When  调用 Run(fakeM)
Then  Run 返回 1
And   fakeM.Run() 不会被继续调用
And   框架记录错误并立刻退出，不会导致 daemon 后台一直残留挂起
```

### TC-09 多容器发生外部环境变量键值冲突

**说明**：如果在环境依赖聚合阶段，框架检测到对注入执行进程的环境变量名发生冲突定义，属于测试执行框架配置失误，此时应 fail-fast 并全量回滚。

```gherkin
Given 构造合法 Config
And   registerHook 返回两个正常的真实容器：
        容器 A 启动成功后，透出了包含 {"DB_ADDR": "A"} 的 SUTEnv
        容器 B 启动成功后，同样透出了包含 {"DB_ADDR": "B"} (产生重名) 的 SUTEnv
When  调用 Run(fakeM)
Then  Run 返回 1
And   fakeM.Run() 不被调用执行
And   Run 失败后抛出冲突错误，daemon 初始化阻断并自行退出清理
```

### TC-10 SUT 就绪探测与业务启动阻塞保护

**说明**：SUT 开启服务探活时，框架承诺给到 `m` 实例的环境不仅拉起成功，更是网络服务完成就绪的，这要求进入 `m.Run()` 之前一直阻塞直到目标探测端口开放。

```gherkin
Given 构造包含启用真实 SUT 探活计划（探测某一端口）的合法 Config
And   真实被测 SUT 进程模拟启动延迟：延迟耗时 300ms 后才尝试开启并绑定目标监听该端口
And   fakeM.Run() 通过内部时间戳器会记录当前执行业务入口的时间戳
When  外部触发调用 Run(fakeM)
Then  Run 整个业务函数调用完成整体耗时 ≥ 300ms
And   fakeM.Run() 中探测到的执行开始时间晚于环境系统真正抛出目标端口已就绪网络侦测时刻
And   Run 正常返回 0 无任何进程泄漏
```

---

- [x] 所有断言均指向外部可观察状态（返回值、runtime 文件清理状态、TCP 端口实际可用性）
- [x] 移除对于底层 testcontainers reaper 重复造轮子的黑盒清理测试，只验证本框架职责内的 daemon 退出与业务服务可用性
- [x] 涉及真实容器的用例均依赖真实 testcontainers-go 启动容器，不使用 mock/stub
- [x] 失败用例的 Then 均可指向具体行为假设
- [x] 覆盖了输入边界、异常恢复、部分成功时的回滚
