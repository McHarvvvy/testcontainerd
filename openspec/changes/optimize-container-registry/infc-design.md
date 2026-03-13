# Capability: starter-owned-connection-contract

# [MODIFY] testcontainerd.Registrar.Register

## 描述
注册接口从“传入框架内置容器配置”调整为“传入用户定义 starter”，由 starter 负责容器启动与标准连接字符串产出。

## 签名
```go
type Registrar interface {
    Register(def container.StarterDefinition) error
}
```

## 参数说明
| Name | Type | Required | Description |
| ---- | ---- | -------- | ----------- |
| def | container.StarterDefinition | Yes | 容器注册定义，必须包含唯一名称与可执行 starter。 |

## 返回值说明
| Name | Type | Required | Description |
| ---- | ---- | -------- | ----------- |
| err | error | Yes | 注册结果；缺失 name/starter、重复 name 时返回错误。 |

## 错误说明

### 注册缺少 name 或 starter
- **错误消息：** `container name is required` / `container starter is required: <name>`

### 注册同名容器
- **错误消息：** `duplicated container name: <name>`

## 备注
- `Register` 保持注册阶段冻结语义不变：daemon 启动后继续拒绝新注册。
- 错误文案继续带容器名上下文，便于调用方定位配置问题。

----

# [ADD] container.StarterDefinition

## 描述
声明单个容器的启动契约，替代 `InstanceConfig` 的 type/spec 驱动方式。

## 签名
```go
type StarterDefinition struct {
    Name    string
    Starter Starter
    Init    InitFunc
}
```

## 参数说明
| Name | Type | Required | Description |
| ---- | ---- | -------- | ----------- |
| Name | string | Yes | 容器唯一名称。 |
| Starter | Starter | Yes | 启动函数，负责启动容器并返回连接结果。 |
| Init | InitFunc | No | 全部容器 starter 成功后的初始化逻辑。 |

## 返回值说明
| Name | Type | Required | Description |
| ---- | ---- | -------- | ----------- |
| - | - | - | 结构体定义，无直接返回值。 |

## 错误说明

### Starter 返回空连接字符串
- **错误消息：** `starter returned empty connection: <name>`

## 备注
- `Starter` 返回值中的 `Connection` 必须为非空白标准连接字符串。
- 框架不再要求或维护 `WithType`、`ContainerType`、`container/spec` 相关输入契约。

----

# [ADD] container.Starter

## 描述
统一 starter 执行函数签名，承接 testcontainers 启动细节与连接字符串生成职责。

## 签名
```go
type Starter func(ctx context.Context, in container.StartInput) (container.StartResult, error)
```

## 参数说明
| Name | Type | Required | Description |
| ---- | ---- | -------- | ----------- |
| ctx | context.Context | Yes | 启动上下文，承载超时与取消信号。 |
| in | container.StartInput | Yes | 启动输入（容器名、可选用户配置、通用 helper）。 |

## 返回值说明
| Name | Type | Required | Description |
| ---- | ---- | -------- | ----------- |
| result | container.StartResult | Yes | 启动结果，至少包含 `Connection`，可附带 `Metadata`。 |
| err | error | Yes | 启动失败错误。 |

## 错误说明

### starter 执行失败
- **错误消息：** `start container <name> failed: <reason>`

## 备注
- 保持并发启动、失败回滚、全量启动后执行 Init 的生命周期语义。
- starter 只定义“如何启动并返回连接结果”，不改变 daemon 的统一回收责任。

----

# Capability: connection-centric-resource-snapshot

# [MODIFY] container.RuntimeResource

## 描述
资源快照模型收敛为连接字符串中心：保留 `Name`，新增必填 `Connection`，`Metadata` 为可选扩展。

## 签名
```go
type RuntimeResource struct {
    Name       string
    Connection string
    Metadata   map[string]string
}
```

## 参数说明
| Name | Type | Required | Description |
| ---- | ---- | -------- | ----------- |
| Name | string | Yes | 资源名称（与注册名一致）。 |
| Connection | string | Yes | 可直接消费的标准连接字符串。 |
| Metadata | map[string]string | No | 扩展字段；可为空。 |

## 返回值说明
| Name | Type | Required | Description |
| ---- | ---- | -------- | ----------- |
| - | - | - | 结构体定义，无直接返回值。 |

## 错误说明

### 连接字符串为空
- **错误消息：** `invalid runtime resource connection: <name>`

## 备注
- `RuntimeView.Get` 与 `Bundle` 对外读取继续返回深拷贝，保证只读快照语义。
- `Host`/`Ports`/`URI`/`Type`/`Image` 等字段不再作为框架资源模型的一部分。

----

# [MODIFY] daemon.StartSUTInput.Resources

## 描述
SUT 启动输入继续保留资源快照字段，但元素类型改为连接中心模型，供 BootPlan 直接消费连接字符串。

## 签名
```go
type StartSUTInput struct {
    Project     string
    RuntimePath string
    Resources   map[string]container.RuntimeResource
}
```

## 参数说明
| Name | Type | Required | Description |
| ---- | ---- | -------- | ----------- |
| Project | string | Yes | 当前项目名。 |
| RuntimePath | string | Yes | runtime 文件路径。 |
| Resources | map[string]container.RuntimeResource | Yes | 内部资源快照，供 SUT 注入使用。 |

## 返回值说明
| Name | Type | Required | Description |
| ---- | ---- | -------- | ----------- |
| - | - | - | 结构体定义，无直接返回值。 |

## 错误说明

### SUT 所需资源缺失
- **错误消息：** `resource <name> not found`

## 备注
- 外部调用方通过 `testcontainerd.StartSUTInput` 类型别名感知同一变更。
- 推荐 SUT 直接消费 `Connection`，避免依赖 host/port 拆分字段。

----

# [MODIFY] /acquire:POST

## 描述
Acquire 响应收敛为租约信息，不再返回资源端点，避免协议层泄漏内部资源模型。

## 参数
| Name | Type | Required | Description |
| ------ | ------ | -------- | ------------ |
| project | string | Yes | 项目标识。 |
| pid | int | Yes | 客户端进程 PID。 |
| run_id | string | No | 一次测试运行标识。 |

## 响应
| Name | Type | Required | Description |
| ----------- | ---- | -------- | ---------------- |
| lease_id | string | Yes | 租约 ID。 |
| acquired_at | string(date-time) | Yes | 租约创建时间。 |

## 错误说明

### 基础设施或 SUT 启动失败
- **HTTP Status:** `500`
- **Error Code:** `internal`
- **Description:** daemon 在 acquire 链路中未能完成 infra/sut 就绪
- **Notes:** 返回体保持 `ErrorResp{code,message}` 结构

## 备注
- 该变更是协议兼容性变更；旧客户端若读取 `resources` 字段需迁移。
- `protocol.ResourceEndpoint` 与 `protocol.AcquireResp.Resources` 一并下线。
