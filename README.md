# testcontainerd

[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Docker](https://img.shields.io/badge/Docker-Required-2496ED?logo=docker&logoColor=white)](https://www.docker.com)

**testcontainerd** 解决 `go test ./...` 多包并发时的容器共享问题。

直接用 testcontainers-go 时，每个测试包独立拉起并销毁容器——10 个包就拉 10 次 Redis，既慢又冲突。testcontainerd 引入一个守护进程（daemon）统一管理容器：第一个测试进程启动 daemon，后续进程自动发现并复用它，所有包共用同一组容器。测试全部结束后，daemon 自动回收容器并退出，不留残留。

```
go test ./pkg/a  ──┐
go test ./pkg/b  ──┤──▶ daemon（HTTP） ──▶ Docker Engine
go test ./pkg/c  ──┘        容器共享
```

---

## 前置要求

- **Go** ≥ 1.24
- **Docker Engine** 可用（`docker ps` 无报错）

---

## 安装

```bash
go get github.com/McHarvvvy/testcontainerd@latest
```

---

## 快速开始

通常在项目的共用 `bootstrap` 包里放两个文件：

### 第一步：注册容器（bootstrap/register.go）

`RegisterContainers` 返回容器列表，每项包含 `Name`（唯一标识）和 `Start`（创建并启动容器的函数）。`Start` 使用 testcontainers-go 的标准 API，和不用 testcontainerd 时写法完全一致。

```go
package bootstrap

import (
    "context"

    "github.com/McHarvvvy/testcontainerd/container"
    "github.com/testcontainers/testcontainers-go"
    "github.com/testcontainers/testcontainers-go/wait"
)

func RegisterContainers(ctx context.Context) ([]container.ContainerRegistration, error) {
    return []container.ContainerRegistration{
        {
            Name: "redis-main",
            Start: func(ctx context.Context) (testcontainers.Container, error) {
                return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
                    ContainerRequest: testcontainers.ContainerRequest{
                        Image:        "redis:7.2-alpine",
                        ExposedPorts: []string{"6379/tcp"},
                        WaitingFor:   wait.ForListeningPort("6379/tcp"),
                    },
                    Started: true,
                })
            },
            // Init: 可选，所有容器全部启动后执行（建库、写种子数据等）
            // Init: func(ctx context.Context) error { return seedDatabase(ctx) },
        },
        // 继续注册 MySQL、MongoDB 等……
    }, nil
}
```

### 第二步：创建启动入口（bootstrap/run.go）

```go
package bootstrap

import (
    "context"
    "log"
    "testing"
    "time"

    "github.com/McHarvvvy/testcontainerd"
    "github.com/McHarvvvy/testcontainerd/container"
)

func Run(m *testing.M) int {
    tcd, err := testcontainerd.New(
        testcontainerd.Config{
            Global: testcontainerd.GlobalConfig{
                Project: "myapp", // 项目标识，用于隔离 runtime 文件
            },
            Daemon: testcontainerd.DaemonConfig{
                Addr:    "127.0.0.1:0",    // :0 自动分配端口
                IdleTTL: 60 * time.Second, // 所有测试完成后 60s 自动退出
            },
            Client: testcontainerd.ClientConfig{
                HTTPTimeout: 1 * time.Minute,
            },
        },
        func(ctx context.Context) ([]container.ContainerRegistration, error) {
            return RegisterContainers(ctx)
        },
    )
    if err != nil {
        log.Printf("testcontainerd.New failed: %v", err)
        return 1
    }
    return tcd.Run(m)
}
```

### 第三步：在各测试包的 TestMain 中调用

```go
package mypackage_test

import (
    "os"
    "testing"

    "your-project/bootstrap"
)

func TestMain(m *testing.M) {
    os.Exit(bootstrap.Run(m))
}
```

**仅此三步**。之后 `go test ./...` 时，所有调用 `bootstrap.Run` 的测试包自动共享容器。

---

## 配置参考

```go
type Config struct {
    Global GlobalConfig
    Daemon DaemonConfig
    Client ClientConfig
    SUT    SUTBootPlan // 可选，nil 表示不托管 SUT
}
```

### GlobalConfig

| 字段          | 类型     | 说明                                                      | 推荐值               |
| ------------- | -------- | --------------------------------------------------------- | -------------------- |
| `Project`     | `string` | 项目标识，决定 runtime 文件隔离路径；空值默认 `"default"` | 项目名，如 `"myapp"` |
| `RuntimePath` | `string` | runtime.json 完整路径；空值按 Project 自动推导            | 留空                 |

### DaemonConfig

| 字段      | 类型            | 说明                           | 推荐值                  |
| --------- | --------------- | ------------------------------ | ----------------------- |
| `Addr`    | `string`        | daemon 监听地址                | `"127.0.0.1:0"`         |
| `Token`   | `string`        | 鉴权令牌；空值自动生成         | 留空                    |
| `IdleTTL` | `time.Duration` | 无活跃请求后自动退出的等待时间 | `60s`（开发期可设更长） |

### ClientConfig

| 字段          | 类型            | 说明                       | 推荐值 |
| ------------- | --------------- | -------------------------- | ------ |
| `HTTPTimeout` | `time.Duration` | 测试进程请求 daemon 的超时 | `1m`   |

---

## SUT 托管（可选）

如果希望框架同时管理被测服务（SUT）进程的生命周期，实现 `SUTBootPlan` 接口并传入 `Config.SUT`。

框架会在所有容器就绪后启动 SUT，探测指定端口可达后才开放测试。SUT 在无活跃请求时自动停止，下次测试时重新拉起。

```go
package bootstrap

import (
    "context"
    "os/exec"
    "time"

    "github.com/McHarvvvy/testcontainerd"
)

type mySUT struct {
    httpAddr string
}

func newSUTBootPlan() testcontainerd.SUTBootPlan {
    return &mySUT{httpAddr: "127.0.0.1:8080"}
}

func (s *mySUT) IsEnable() bool               { return true }
func (s *mySUT) GetIdleTTL() time.Duration     { return 10 * time.Second }
func (s *mySUT) GetReadyTimeout() time.Duration { return 30 * time.Second }
func (s *mySUT) GetGracePeriod() time.Duration  { return 5 * time.Second }

// GetProbeAddrs 返回就绪探测地址列表；daemon 轮询这些端口直到全部可达。
// 返回 nil 表示不探测端口，仅检查进程未立即退出即认为启动成功。
func (s *mySUT) GetProbeAddrs() []string {
    return []string{s.httpAddr}
}

// GetCommand 构建 SUT 启动命令。
// in.Project 和 in.RuntimePath 提供当前项目上下文。
// 如需将容器连接地址注入 SUT，在这里查询 Docker 并写入 cmd.Env。
func (s *mySUT) GetCommand(ctx context.Context, in testcontainerd.StartSUTInput) (*exec.Cmd, error) {
    cmd := exec.CommandContext(ctx, "./bin/myapp", "--listen", s.httpAddr)
    cmd.Dir = "/path/to/project"
    cmd.Env = append(os.Environ(),
        "REDIS_ADDR=127.0.0.1:6379", // 从 Docker 查询容器端口后注入
    )
    return cmd, nil
}
```

然后在 `Run()` 中传入：

```go
testcontainerd.Config{
    // ...
    SUT: newSUTBootPlan(),
}
```

**空闲回收**：最后一个测试完成后，先等 `GetIdleTTL()` 秒停止 SUT，再等 `DaemonConfig.IdleTTL` 秒销毁容器并退出 daemon。建议 SUT IdleTTL 设小、Daemon IdleTTL 设大，这样容器持续可用而 SUT 及时释放端口。这样能多次快速得到测试反馈

---

## 日志与排障

运行时产物默认存放在：

```
{os.TempDir()}/tcd/{project}/
```

| 文件           | 内容                                    |
| -------------- | --------------------------------------- |
| `runtime.json` | daemon 地址、token、PID                 |
| `daemon.log`   | daemon 进程日志                         |
| `app.log`      | SUT 的 stdout/stderr（启用 SUT 时生成） |

| 问题                        | 排查方法                                                             |
| --------------------------- | -------------------------------------------------------------------- |
| daemon 启动失败             | 查 `daemon.log`                                                      |
| SUT 不就绪或意外退出        | 查 `app.log`                                                         |
| 容器名冲突                  | daemon 启动前自动清理同名容器；确认没有手工创建的同名容器            |
| 多 daemon 并发启动          | 不要手动删除 `runtime.json.start.lock`，除非确认无 daemon 进程在运行 |
| Windows 下 TempDir 清理失败 | 正常退出时 `runner/` 目录自动清理；异常退出可手动删除                |

## 注意
设计时没有考虑并发测试的外部状态隔离机制，所以最好不要使用 t.parallel() 进行并行测试
