# f50_pro_relay

用于 ZTE F50Pro / MU3356 的 Android WiFi 中继 Go 包。宿主通过 `import` 使用，在自己的进程中管理 WiFi 连接、DHCP、联网探测和客户端出口切换；不启动独立服务、HTTP 接口或 RPC，也不注册信号处理器。

## 安装

```sh
go get github.com/hualinli/f50_pro_relay@v0.5.1
```

Go 包名为 `relay`，最低 Go 版本为 1.24；本仓库使用 Go 1.27.2 构建和验证。

```go
import (
    "context"
    "time"

    "github.com/hualinli/f50_pro_relay"
)

func run(ctx context.Context) error {
    manager, err := relay.New(relay.Options{
        BaseDir:    "/data/adb/f50-pro-panel",
        RuntimeDir: "/data/vendor/wifi/f50-pro-relay",
        Tools: relay.Tools{
            IW: "/data/adb/f50-pro-panel/iw",
        },
    })
    if err != nil {
        return err
    }
    ctx, cancel := context.WithCancel(ctx)
    defer cancel()
    done := make(chan error, 1)
    go func() { done <- manager.Run(ctx) }()
    startup, stopWaiting := context.WithTimeout(ctx, 30*time.Second)
    err = manager.Ready(startup)
    stopWaiting()
    if err != nil {
        cancel()
        <-done
        return err
    }
    // 在宿主的处理函数中调用 manager.Status / Scan / SaveProfile 等。
    <-ctx.Done()
    cancel()
    return <-done
}
```

完整生命周期示例见 [example_test.go](example_test.go)。`New` 不修改设备；`Run` 先检查依赖工具是否存在且可执行，再开始初始化。一个 Manager 只能运行一次。停止时取消传给 `Run` 的 context，并等待它返回，确认子进程与网络清理结束。

## 设备依赖

需要 root 及相应网络管理权限、支持 AP + STA 并发的驱动，以及以下工具：

| 工具 | 默认路径 |
| --- | --- |
| iw | `BaseDir/iw`，由宿主单独部署 |
| wpa_supplicant | `/vendor/bin/hw/wpa_supplicant` |
| BusyBox（含 udhcpc） | `/data/adb/magisk/busybox` |
| ip / iptables / ip6tables | `/system/bin/` |

可通过 `Options.Tools` 指定绝对路径。本仓库不包含这些工具的二进制。Magisk 修补镜像本身不一定会补齐 `/data/adb/magisk`，安装前需要确认运行环境完整。SELinux enforcing 环境的额外策略适配尚未验证。

`BaseDir` 为 root 私有目录，`RuntimeDir` 为 wifi 用户私有目录，不能相同或互相嵌套。默认保留旧安装路径 `/data/adb/ufi-relay` 和 `/data/vendor/wifi/ufi-relay`，新宿主建议显式设置自己的目录。

## 接口

| 方法 | 用途 |
| --- | --- |
| `Run` / `Ready` | 启动、等待初始化 |
| `Status` / `Profiles` | 出口、租约、近期事件及不含密码的配置 |
| `Scan` | 发起扫描；返回 `ScanSnapshot`，稍后再次读取结果 |
| `SaveProfile` / `DeleteProfile` | 保存或忘记网络 |
| `Connect` / `Reconnect` | 手动选择或重新连接 |
| `SetMode` | `ModeAuto`、`ModeCellular`、`ModeWiFi` |
| `RenewLease` | 请求 DHCP 续租 |
| `Observe` | 按需读取链路参数和流量计数 |
| `TestDisconnect` | 暂停重连 15 秒，演练断链回退 |

方法支持并发调用，网络操作在内部排队。返回的配置、状态和扫描结果为独立快照。取消请求会跳过尚未执行的操作；已经开始的操作可能完成，不代表自动回滚。生命周期外返回 `ErrNotRunning`。

`ImportConfig` 用于离线导入；`Recover` 用于宿主监管进程清理异常退出后的资源。它们使用与运行实例相同的安装锁，拒绝修改正在运行的实例。宿主应在工作进程异常退出后调用 `Recover`，再创建新 Manager；进程被 SIGKILL 时不会执行 Go defer。

## 网络行为

- 针对 F50Pro 的 `br0`、`phy0`、UID 1010，以及专用 STA 接口 `relay_sta0`、路由表 30555、策略优先级 10950/10960 和 `UFI_RELAY*` 防火墙链。同一设备仅运行一个 Manager，即使使用不同数据目录也不能并行运行两个实例。
- 下游 IPv4 地址和前缀动态读取。LAN 地址或桥编号变化时清理旧规则、重新获取租约；上下游网段冲突时拒绝启用。
- 自动模式结合关联状态、DHCP 租约和联网检查决定切换；失败回退，恢复有冷却。切换会清理相关客户端连接跟踪，现有 TCP / QUIC 连接可能重建。
- WiFi 出口策略针对下游客户端转发；模块自身的联网探测绑定 STA。不会自动把 Android 所有应用、原厂 OTA 或系统 DNS 切到 WiFi，宿主如有该需求需另行实现。
- WiFi 完整 IPv6 转发尚未实现，WiFi 出口启用期间阻止下游通过蜂窝 IPv6 旁路。
- 网页认证网络可由手动 WiFi 模式尝试接入，但真实门户网络、长期弱信号和实地覆盖范围切换仍需验证。

恢复使用私有所有权记录，只清理本实例的子进程和网络资源。兼容旧 `ufi_sta0` 与旧版固定 LAN 的恢复记录；升级前应停止旧实例并完成清理。

## 配置与观测

WiFi 密码保存在 root 0600 的 `config.json` 与 wifi 0600 的运行配置中，不出现在公开状态或正常日志中。BusyBox udhcpc 通过一小段 sh 回调原子写入租约事件；认证、地址验证和路由由 Go 处理，不需要宿主实现 DHCP 回调命令或开放端口。

`Observe` 返回 RSSI、信道、带宽、协商速率、MCS/NSS/GI/DCM、接口编号和当前收发计数。它不扫描、不改网络、不添加采样循环。可用 `errors.Is` 区分 `ErrLinkUnavailable` 与 `ErrCountersUnavailable`；失败时可能返回部分数据。`ReadObservation` 可用于不启动 Manager 的只读诊断。

`Status.Traffic` 是检测到关联时的内存基线。宿主通过同接口当前计数相减计算累计；断线、换网或服务重启后重建，计数回退时应显示不可用。这些计数包含局域网及设备自身流量，不作为计费依据。

## 验证与源码包

```sh
go test -race ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
```

macOS 可构建和运行纯逻辑测试，实际中继必须在目标 Android/Linux 上验证。相邻 `f50_pro_panel` 项目是使用本包的宿主之一，不是本包的依赖。

在干净的 Git 工作树执行 `sh tools/build-package.sh`，生成 `build/f50_pro_relay-v0.5.1-source.tar.gz` 与 SHA-256 校验文件。源码包只包含当前已提交文件，不收集被忽略或未跟踪的本地文件；解压后可独立测试和编译。

### v0.5.1 验证范围

已通过 Go 1.27.2 的竞态测试、静态检查和 Linux ARM64 编译；通过 F50Pro B28 / Magisk 30.7 临时 panel 宿主验证启动、状态、无线扫描、按需观测和停止清理。依赖缺失时未执行外部工具或创建安装目录的回归测试通过；源码打包的忽略文件排除和脏工作树拒绝测试通过。

本轮未重新验证真实上游连接、DHCP 续租、自动断线回退、弱信号或长时间运行。该版本用于受控设备调试，不将早期固件上的历史测试等同于 B28 完整验收。
