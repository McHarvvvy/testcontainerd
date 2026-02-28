# testcontainerd

[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Docker](https://img.shields.io/badge/Docker-Required-2496ED?logo=docker&logoColor=white)](https://www.docker.com)
[![License](https://img.shields.io/badge/License-Internal-lightgrey)]()

**testcontainerd** 是一个面向 Go 集成测试的容器编排框架。它通过 **daemon-client 架构** 在本地管理测试依赖容器（MySQL、Redis、MongoDB、Pulsar 等）与可选的 SUT（System Under Test，被测服务）进程，使集成测试具备 **可复用、可并发、可自动回收** 的运行环境。

---

## 目录

- [testcontainerd](#testcontainerd)
  - [目录](#目录)
  - [定位与目标](#定位与目标)
  - [核心特性](#核心特性)
  - [架构概览](#架构概览)
  - [前置要求](#前置要求)
  - [安装](#安装)
  - [快速开始](#快速开始)
  - [使用指南](#使用指南)
    - [Step 1：注册容器](#step-1注册容器)
    - [Step 2：编写启动入口](#step-2编写启动入口)
    - [Step 3：在 TestMain 中调用](#step-3在-testmain-中调用)
    - [Step 4（可选）：启用 SUT 托管](#step-4可选启用-sut-托管)
  - [内置容器驱动](#内置容器驱动)
  - [配置参考](#配置参考)
    - [Config](#config)
    - [GlobalConfig](#globalconfig)
    - [DaemonConfig](#daemonconfig)
    - [ClientConfig](#clientconfig)
    - [SUTBootPlan 接口](#sutbootplan-接口)
  - [容器实例选项 API](#容器实例选项-api)
  - [典型使用场景](#典型使用场景)
    - [场景一：仅容器编排，SUT 自行管理](#场景一仅容器编排sut-自行管理)
    - [场景二：容器 + SUT 全托管](#场景二容器--sut-全托管)
  - [运行时产物与日志](#运行时产物与日志)
  - [工作原理](#工作原理)
    - [双模式运行](#双模式运行)
    - [租约生命周期](#租约生命周期)
    - [Daemon 自启动与并发安全](#daemon-自启动与并发安全)
    - [空闲回收策略](#空闲回收策略)
  - [跨平台支持](#跨平台支持)
  - [排障指南](#排障指南)
  - [包结构](#包结构)

---

## 定位与目标

testcontainerd **不是**通用容器管理平台，而是集成测试场景下的 **测试运行时协调器**：

1. **一行接入** —— 在 `TestMain` 中调用一次，框架自动完成容器拉起、SUT 启动、资源注入。
2. **跨包复用** —— `go test -count=1 ./...` 执行多个测试包时，首个进程拉起 daemon，后续进程自动发现并复用，避免重复创建容器。
3. **自动回收** —— 基于租约（lease）和空闲 TTL 机制，测试结束后自动销毁容器和 daemon 进程，不留资源残留。

---

## 核心特性

| 特性                   | 说明                                                                                               |
| :--------------------- | :------------------------------------------------------------------------------------------------- |
| **容器注册与并发启动** | 通过 `Registry` 注册任意数量容器实例，`Bundle` 以最多 4 并发启动并统一回滚失败分支                 |
| **Spec 驱动抽象**      | 内置 MySQL / Redis / MongoDB / Pulsar 驱动，提供默认端口、环境变量、就绪策略和连接串生成           |
| **Init 函数**          | 容器全部启动后执行自定义初始化（建库、建表、创建 Topic 等），可通过 `RuntimeView` 查询其他容器信息 |
| **租约保活**           | client 获取 lease 后自动心跳，daemon 按固定 TTL（默认 2s）清理过期 lease                           |
| **Daemon 自启动**      | client 优先复用已有 daemon；不可用时通过文件锁安全自启动，天然支持并发测试场景                     |
| **SUT 生命周期管理**   | 可选托管被测服务进程：编译 → 拉起 → 端口探测 → 就绪，支持空闲回收与自动重启                        |
| **跨平台进程回收**     | Unix 使用进程组（pgid），Windows 使用 JobObject + `taskkill` 兜底                                  |
| **孤儿容器清理**       | daemon 启动前自动清理同名残留容器，避免 Docker 层名称冲突                                          |

---

## 架构概览

```
┌──────────────────────────────────────────────────────────────────┐
│                          go test ./...                           │
│  ┌─────────┐   ┌─────────┐   ┌─────────┐                       │
│  │ Test Pkg │   │ Test Pkg │   │ Test Pkg │  ... (N 个测试进程)   │
│  │ (client) │   │ (client) │   │ (client) │                     │
│  └────┬─────┘   └────┬─────┘   └────┬─────┘                    │
│       │    Acquire/    │  Heartbeat/  │  Release                 │
│       └───────┬────────┴──────┬──────┘                          │
│               ▼               ▼                                  │
│     ┌─────────────────────────────────┐                         │
│     │           Daemon (HTTP)          │◀── runtime.json 发现    │
│     │  ┌─────────┐   ┌────────────┐   │                        │
│     │  │  Lease   │   │   Bundle   │   │                        │
│     │  │  Store   │   │ (容器管理) │   │                        │
│     │  └─────────┘   └────┬───────┘   │                        │
│     │       ┌──────────────┤          │                         │
│     │       ▼              ▼          │                         │
│     │  ┌─────────┐   ┌──────────┐    │                         │
│     │  │   SUT   │   │  Docker  │    │                         │
│     │  │ Manager │   │  Engine  │    │                         │
│     │  └─────────┘   └──────────┘    │                         │
│     └─────────────────────────────────┘                         │
└──────────────────────────────────────────────────────────────────┘
```

---

## 前置要求

- **Go** ≥ 1.24（与你的项目版本保持一致即可）
- **Docker Engine** 可用（`testcontainers-go` 依赖其 API）

---

## 安装

```bash
go get github.com/McHarvvvy/testcontainerd@latest
```

---

## 快速开始

最简接入方式：在测试包的 `TestMain` 中一行调用。

```go
package mypackage_test

import (
    "os"
    "testing"

    "your-project/bootstrap" // 你自己封装的启动包
)

func TestMain(m *testing.M) {
    os.Exit(bootstrap.Run(m))
}
```

`bootstrap.Run` 内部完成三件事：

1. 构建 `testcontainerd.Config`（Global / Daemon / Client / SUT）。
2. 在注册钩子中调用容器注册函数。
3. 调用 `tcd.Run(m)` —— 框架自动完成 daemon 发现/启动、容器拉起、SUT 就绪探测、运行测试、资源释放。

---

## 使用指南

### Step 1：注册容器

使用 `container.NewInstance` + `WithXXX` 选项函数声明容器实例：

```go
package bootstrap

import (
    "context"
    "fmt"

    "github.com/McHarvvvy/testcontainerd"
    "github.com/McHarvvvy/testcontainerd/container"
)

func RegisterContainers(reg testcontainerd.Registrar) error {
    // 注册 MySQL 容器
    if err := reg.Register(container.MustNewInstance(
        "mysql-main",
        container.WithType(container.TypeMySQL),
        container.WithImage("mysql:8.0.36"),
        container.WithPort("mysql", 3306, 0),                // hostPort=0 表示随机映射
        container.WithEnv("MYSQL_ROOT_PASSWORD", "pass"),
        container.WithInit(func(ctx context.Context, in container.InitInput) error {
            // 容器全部启动后执行初始化：建库、建表等
            // in.Self   → 当前容器的运行时信息（Host, Ports, Metadata, URI）
            // in.Runtime → 查看其他已注册容器的运行时信息
            return nil
        }),
    )); err != nil {
        return err
    }

    // 注册 Redis 容器
    if err := reg.Register(container.MustNewInstance(
        "redis-main",
        container.WithType(container.TypeRedis),
        container.WithImage("redis:7.2-alpine"),
    )); err != nil {
        return err
    }

    return nil
}
```

**关键规则**：

- **`Name` 必须唯一**，注册表在注册时检查重复名称，并检测 hostPort 冲突。
- **`HostPort = 0`** 表示随机端口映射，推荐使用以避免端口冲突。
- **`WithInit`** 在 **所有容器启动完成后** 统一执行，初始化函数可通过 `RuntimeView` 查询其他容器的连接信息。

### Step 2：编写启动入口

```go
package bootstrap

import (
    "context"
    "log"
    "testing"
    "time"

    "github.com/McHarvvvy/testcontainerd"
)

func Run(m *testing.M) int {
    tcd, err := testcontainerd.New(
        testcontainerd.Config{
            Global: testcontainerd.GlobalConfig{
                Project: "my_project",     // 项目标识，用于 runtime 文件隔离
            },
            Daemon: testcontainerd.DaemonConfig{
                Addr:    "127.0.0.1:0",    // 随机端口，避免冲突
                IdleTTL: 60 * time.Second, // daemon 空闲 60s 后自动退出
            },
            Client: testcontainerd.ClientConfig{
                HTTPTimeout: 1 * time.Minute,
            },
            // SUT: newSUTBootPlan(),  // 可选：启用 SUT 托管
        },
        func(ctx context.Context, r testcontainerd.Registrar) error {
            return RegisterContainers(r)
        },
    )
    if err != nil {
        log.Printf("testcontainerd.New failed: %v", err)
        return 1
    }
    return tcd.Run(m)
}
```

### Step 3：在 TestMain 中调用

```go
func TestMain(m *testing.M) {
    os.Exit(bootstrap.Run(m))
}
```

框架会根据环境变量 `TCD_MODE` 自动判断当前进程角色：
- **无 `TCD_MODE`** → client 模式：发现/启动 daemon → 获取 lease → 运行测试 → 释放 lease。
- **`TCD_MODE=daemon`** → daemon 模式：注册容器 → 启动 HTTP 服务 → 等待 client 请求。

### Step 4（可选）：启用 SUT 托管

实现 `SUTBootPlan` 接口并传入 `Config.SUT`：

```go
type sutBootPlan struct {
    // ...
}

func (p *sutBootPlan) IsEnable() bool              { return true }
func (p *sutBootPlan) GetIdleTTL() time.Duration   { return 2 * time.Second }
func (p *sutBootPlan) GetReadyTimeout() time.Duration { return 30 * time.Second }
func (p *sutBootPlan) GetGracePeriod() time.Duration  { return 5 * time.Second }

func (p *sutBootPlan) GetCommand(ctx context.Context, in testcontainerd.StartSUTInput) (*exec.Cmd, error) {
    // 构建 SUT 启动命令，可利用 in.Resources 获取容器连接信息注入环境变量
    cmd := exec.CommandContext(context.Background(), "/path/to/binary")
    cmd.Dir = "/path/to/workdir"   // 必须设置工作目录
    cmd.Env = buildEnv(in.Resources)
    return cmd, nil
}

func (p *sutBootPlan) GetProbeAddrs() []string {
    // 返回 SUT 就绪探测地址，daemon 会轮询这些端口直到全部可达
    return []string{"127.0.0.1:8080", "127.0.0.1:9090"}
}

func (p *sutBootPlan) SetEnvEndpoint() error {
    // client 模式下向测试进程注入 SUT 访问地址
    return os.Setenv("APP_HTTP_ADDR", "127.0.0.1:8080")
}
```

**注意**：
- `GetCommand` 返回的 `*exec.Cmd` **必须设置 `Dir`（工作目录）**。
- SUT 进程的 stdout/stderr 会自动重定向到 `app.log`，便于排障。
- `GetProbeAddrs` 返回空列表时，框架仅检查进程短时间内未立即退出即视为启动成功。

完整示例参见 [`examples/`](./examples/) 目录。

---

## 内置容器驱动

所有内置驱动通过 `container/spec` 包的 `init()` 自动注册，无需手动调用。

| 类型        | 常量                   | 默认端口           | 就绪策略                | 连接串格式                            |
| :---------- | :--------------------- | :----------------- | :---------------------- | :------------------------------------ |
| **MySQL**   | `container.TypeMySQL`  | 3306/tcp           | 端口监听（120s 超时）   | `user:pass@tcp(host:port)/...`        |
| **Redis**   | `container.TypeRedis`  | 6379/tcp           | 端口监听（90s 超时）    | `redis://host:port`                   |
| **MongoDB** | `container.TypeMongo`  | 27017/tcp          | 端口监听（120s 超时）   | `mongodb://user:pass@host:port/admin` |
| **Pulsar**  | `container.TypePulsar` | 6650/tcp, 8080/tcp | 双端口监听（180s 超时） | `pulsar://host:port`                  |

每个驱动自动处理：
- 默认环境变量注入（如 `MYSQL_ROOT_PASSWORD`）
- 连接元数据提取（user / password）
- 统一 URI 生成

---

## 配置参考

### Config

```go
type Config struct {
    Global GlobalConfig
    Daemon DaemonConfig
    Client ClientConfig
    SUT    SUTBootPlan  // 可选，nil 或 IsEnable() == false 时不托管 SUT
}
```

### GlobalConfig

| 字段          | 类型     | 默认值                                 | 说明                                              |
| :------------ | :------- | :------------------------------------- | :------------------------------------------------ |
| `Project`     | `string` | `"default"`                            | 项目隔离标识，用于 runtime 文件路径和日志目录隔离 |
| `RuntimePath` | `string` | `{tempdir}/tcd/{project}/runtime.json` | runtime 文件路径，空值按 Project 自动推导         |

### DaemonConfig

| 字段      | 类型            | 默认值          | 说明                                        |
| :-------- | :-------------- | :-------------- | :------------------------------------------ |
| `Addr`    | `string`        | `"127.0.0.1:0"` | daemon 监听地址，建议使用 `:0` 自动分配端口 |
| `Token`   | `string`        | 自动生成        | 鉴权令牌，client 请求需携带匹配值           |
| `IdleTTL` | `time.Duration` | `60s`           | daemon 无活跃 lease 时的自动退出等待时间    |

### ClientConfig

| 字段          | 类型            | 默认值 | 说明                            |
| :------------ | :-------------- | :----- | :------------------------------ |
| `HTTPTimeout` | `time.Duration` | `1m`   | client 请求 daemon 的 HTTP 超时 |

### SUTBootPlan 接口

```go
type SUTBootPlan interface {
    IsEnable() bool                                                        // 是否启用 SUT 管理
    GetIdleTTL() time.Duration                                             // SUT 空闲回收等待时长
    GetReadyTimeout() time.Duration                                        // SUT 就绪探测超时（默认 8s）
    GetGracePeriod() time.Duration                                         // 停止 SUT 的优雅退出等待时长（默认 4s）
    GetCommand(ctx context.Context, in StartSUTInput) (*exec.Cmd, error)   // 返回 SUT 启动命令
    GetProbeAddrs() []string                                               // 就绪探测地址列表（ip:port）
    SetEnvEndpoint() error                                                 // client 模式下注入 SUT 访问地址到环境变量
}
```

`StartSUTInput` 提供以下信息：

| 字段          | 类型                          | 说明                                |
| :------------ | :---------------------------- | :---------------------------------- |
| `Project`     | `string`                      | 当前项目名                          |
| `RuntimePath` | `string`                      | daemon runtime 文件路径             |
| `Resources`   | `map[string]ResourceEndpoint` | 已启动容器的连接信息（名称 → 端点） |

---

## 容器实例选项 API

| 函数                                                                  | 用途                                                                   |
| :-------------------------------------------------------------------- | :--------------------------------------------------------------------- |
| `container.NewInstance(name, opts...)`                                | 创建容器实例配置，返回 `(InstanceConfig, error)`                       |
| `container.MustNewInstance(name, opts...)`                            | 同上，失败时 panic                                                     |
| `container.WithType(t)`                                               | 设置容器类型（`TypeMySQL` / `TypeRedis` / `TypeMongo` / `TypePulsar`） |
| `container.WithImage(image)`                                          | 设置 Docker 镜像                                                       |
| `container.WithPort(name, containerPort, hostPort)`                   | 添加端口映射，`hostPort=0` 为随机映射                                  |
| `container.WithPortProtocol(name, containerPort, protocol, hostPort)` | 添加端口映射并指定协议                                                 |
| `container.WithEnv(key, value)`                                       | 设置容器环境变量                                                       |
| `container.WithInit(fn)`                                              | 设置容器初始化函数                                                     |

**InitFunc 签名**：

```go
type InitFunc func(ctx context.Context, in InitInput) error

type InitInput struct {
    Self    RuntimeResource  // 当前容器的运行时信息
    Runtime RuntimeView      // 查询其他容器运行时信息的只读视图
}
```

---

## 典型使用场景

### 场景一：仅容器编排，SUT 自行管理

适用于容器生命周期由框架托管，但 SUT 启停由业务脚本或 IDE 手动控制的情况。

```go
tcd, _ := testcontainerd.New(
    testcontainerd.Config{
        Global: testcontainerd.GlobalConfig{Project: "my_project"},
        Daemon: testcontainerd.DaemonConfig{
            IdleTTL: 1 * time.Hour,  // 设置较长的空闲时间，方便开发调试
        },
        SUT: nil,  // 不托管 SUT
    },
    func(ctx context.Context, r testcontainerd.Registrar) error {
        return RegisterContainers(r)
    },
)
```

> 只要 daemon 不超过 IdleTTL 退出，容器就持续可用。适合反复运行测试的开发阶段。

### 场景二：容器 + SUT 全托管

适用于希望测试用例拿到环境时，容器和 SUT 均已 ready 的全自动模式。

```go
tcd, _ := testcontainerd.New(
    testcontainerd.Config{
        Global: testcontainerd.GlobalConfig{Project: "my_project"},
        Daemon: testcontainerd.DaemonConfig{
            IdleTTL: 60 * time.Second,
        },
        SUT: newSUTBootPlan(),  // 实现 SUTBootPlan 接口
    },
    func(ctx context.Context, r testcontainerd.Registrar) error {
        return RegisterContainers(r)
    },
)
```

**关于空闲回收策略**：

daemon 使用双层空闲回收机制：

1. **SUT 层**：最后一个 lease 释放后，等待 `SUTBootPlan.GetIdleTTL()` 后先回收 SUT 进程。
2. **Daemon 层**：再等待 `Daemon.IdleTTL` 后回收容器并退出 daemon 进程。

> **建议**：设置较大的 `Daemon.IdleTTL` 和较小的 `SUT.GetIdleTTL()`。这样容器长时间保持可用（避免重复拉镜像），而 SUT 尽快释放端口并在下次测试时重新编译。

---

## 运行时产物与日志

所有运行时中间产物存放在 runtime 目录下，由 `GlobalConfig.RuntimePath` 决定。默认路径为：

```
{os.TempDir()}/tcd/{project}/
```

| 文件/目录                 | 说明                                           |
| :------------------------ | :--------------------------------------------- |
| `runtime.json`            | daemon 发现文件：地址、token、PID、启动时间    |
| `daemon.log`              | daemon 进程日志                                |
| `app.log`                 | SUT 进程的 stdout/stderr（启用 SUT 时生成）    |
| `runner/`                 | Windows 专用：daemon runner 临时可执行文件目录 |
| `runtime.json.start.lock` | 自启动文件锁（正常退出后自动删除）             |

---

## 工作原理

### 双模式运行

`testcontainerd.Run(m)` 根据环境变量 `TCD_MODE` 判断进程角色：

| 环境变量          | 角色       | 行为                                                              |
| :---------------- | :--------- | :---------------------------------------------------------------- |
| 未设置            | **Client** | 发现/启动 daemon → 获取 lease → 运行 `m.Run()` → 释放 lease       |
| `TCD_MODE=daemon` | **Daemon** | 注册容器 → 监听 HTTP → 按需拉起容器和 SUT → 管理 lease → 空闲退出 |

### 租约生命周期

```
Client                          Daemon
  │                               │
  │──── POST /v1/acquire ────────▶│  分配 lease，返回资源端点
  │◀─── AcquireResp ─────────────│
  │                               │
  │──── POST /v1/heartbeat ──────▶│  每 1s 续租（LeaseTTL = 2s）
  │──── POST /v1/heartbeat ──────▶│
  │        ...                    │
  │                               │
  │──── POST /v1/release ────────▶│  释放 lease
  │                               │
  │                               │── 无活跃 lease 超过 IdleTTL ──▶ 自动退出
```

### Daemon 自启动与并发安全

当 `go test ./...` 并发运行多个测试进程时：

1. 首个 client 发现无 `runtime.json` → 通过 `O_CREATE|O_EXCL` 排他创建 `.start.lock` 文件抢锁。
2. 抢到锁的 client 以 daemon 模式重启当前测试二进制 → 等待 `runtime.json` 就绪 → 连接。
3. 未抢到锁的 client 轮询等待 `runtime.json` 出现 → 直接连接已有 daemon。
4. 锁文件超时 90s 且无 runtime 时视为陈旧锁，允许清理后重试。

### 空闲回收策略

daemon 内部 reaper goroutine 每 2s 轮询：

1. 存在活跃 lease 或 Acquire 请求进行中 → 重置空闲计时。
2. SUT 空闲超过 `SUTBootPlan.GetIdleTTL()` → 先停止 SUT。
3. Daemon 空闲超过 `Config.IdleTTL` → 停止 SUT → 停止容器 → 关闭 HTTP → 清理 runtime 文件 → 退出。

---

## 跨平台支持

| 平台              | SUT 进程管理                                                             | 额外处理                                  |
| :---------------- | :----------------------------------------------------------------------- | :---------------------------------------- |
| **Linux / macOS** | 独立进程组（`Setpgid`），通过 `kill -pgid` 终止子进程树                  | —                                         |
| **Windows**       | JobObject（`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`）+ `taskkill /T /F` 兜底 | daemon 启动时复制测试二进制避免文件锁冲突 |

---

## 排障指南

| 问题                  | 排查方法                                                                   |
| :-------------------- | :------------------------------------------------------------------------- |
| daemon 启动失败       | 查看 `{runtime_dir}/daemon.log`                                            |
| SUT 启动失败          | 查看 `{runtime_dir}/app.log`                                               |
| 容器名冲突            | daemon 启动前会自动清理同名容器；确认没有手工创建的同名容器                |
| 并发启动多个 daemon   | 不要手动删除 `.start.lock` 文件，除非确认无 daemon 进程存活                |
| `WithInit` 失败       | 该行为会触发所有容器回滚并终止本次 acquire，这是预期行为；请检查初始化逻辑 |
| lease 过期 / 心跳失败 | 心跳失败不会中断测试，lease 过期后由 daemon 的 GC 机制兜底回收             |
| Windows 下残留 runner | 正常退出时 runner 目录会清理；异常退出可手动删除 `{runtime_dir}/runner/`   |

> **建议**：SUT 探测地址明确写 `127.0.0.1:port` 格式，避免仅写 `:port` 带来的歧义。

---

## 包结构

```
testcontainerd/
├── testcontainerd.go        # 统一入口：Config、New()、Run()
├── go.mod
│
├── client/                  # client 子包
│   ├── client.go            #   Client 结构体、Acquire/Heartbeat/Release/State
│   ├── autostart.go         #   daemon 自启动逻辑（文件锁、进程管理）
│   └── heartbeat.go         #   租约心跳 goroutine
│
├── daemon/                  # daemon 子包
│   ├── server.go            #   HTTP 服务、API Handler、reaper 循环
│   ├── lifecycle.go         #   容器与 SUT 启停入口
│   ├── lease_store.go       #   lease 存储与 GC
│   ├── cleanup.go           #   孤儿容器清理
│   ├── sut_manager.go       #   SUT 进程管理（启动、探测、停止）
│   ├── process_control_unix.go     # Unix 进程组管理
│   └── process_control_windows.go  # Windows JobObject 管理
│
├── container/               # 容器管理子包
│   ├── options.go           #   InstanceConfig、Option 函数（WithType/WithImage/...）
│   ├── registry.go          #   容器注册表（名称去重、端口冲突检测）
│   ├── bundle.go            #   容器生命周期编排（并发启动、Init、回滚）
│   ├── drivers.go           #   Docker 容器创建与重试
│   ├── runtime_view.go      #   运行时资源只读视图
│   └── spec/                #   内置容器驱动
│       ├── spec.go          #     Driver 接口与全局注册
│       ├── mysql.go         #     MySQL 驱动
│       ├── redis.go         #     Redis 驱动
│       ├── mongo.go         #     MongoDB 驱动
│       └── pulsar.go        #     Pulsar 驱动
│
├── protocol/                # 通信协议
│   ├── const.go             #   API 路径与 Header 常量
│   ├── types.go             #   请求/响应结构体
│   └── error.go             #   错误码定义
│
├── constant/                # 公共常量
│   ├── container.go         #   容器类型、端口名、环境变量名
│   ├── env.go               #   环境变量 key
│   └── lease.go             #   LeaseTTL、HeartbeatInterval
│
├── tcdruntime/              # 运行时文件管理
│   ├── file.go              #   runtime.json 读写与等待
│   ├── path.go              #   路径推导（runtime、artifact、runner）
│   └── log.go               #   日志文件管理
│
├── examples/                # 使用示例
│   ├── run.go               #   TestMain 启动入口
│   ├── register.go          #   容器注册与初始化
│   └── sut.go               #   SUTBootPlan 实现
│
└── docs/
    └── README.md            # 本文档
```
