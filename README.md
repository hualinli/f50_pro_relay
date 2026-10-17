# f50_pro_relay：可独立分发的 WiFi 中继模块

导入 `f50_pro_relay`（Go 包名为 `relay`），通过 `relay.New(options)` 创建 `*relay.Manager`。这个包负责 WiFi、租约、联网探测和出口切换，不注册退出信号，不启动网页或管理 RPC，也不调用 `os.Exit`。

```go
ctx, cancel := context.WithCancel(parentCtx)
defer cancel()

manager, err := relay.New(relay.Options{
    BaseDir:    "/data/adb/ufi-relay",
    RuntimeDir: "/data/vendor/wifi/ufi-relay",
    Logger:     logger, // *slog.Logger；可省略，默认不输出日志
})
if err != nil {
    return err
}

done := make(chan error, 1)
go func() { done <- manager.Run(ctx) }()
if err := manager.Ready(ctx); err != nil {
    cancel() // ctx 由宿主通过 context.WithCancel 创建
    <-done
    return err
}
status, err := manager.Status(ctx)
```

完整、可编译的生命周期示例见 `example_test.go`。宿主负责创建并取消 `ctx`，以及等待 `Run` 返回；取消后会停止子进程并清理本实例拥有的网络规则。一个 Manager 只能 Run 一次，重新启动需创建新实例。

## 接口

| 方法 | 用途 |
| --- | --- |
| `Run(ctx)` / `Ready(ctx)` | 启动与等待初始化完成 |
| `Observe(ctx)` | 按需读取上游链路参数和当前收发计数，返回 `Observation` 与可识别的读取错误 |
| `Status(ctx)` | 当前状态、出口、租约及最近事件，不含 WiFi 密码 |
| `Profiles(ctx)` | 已保存网络的公开信息 |
| `Scan(ctx)` | 发起扫描并返回 `ScanSnapshot{Pending, Networks}`；稍后再次调用读取结果 |
| `SaveProfile(ctx, profile)` | 按 ID 或 SSID 保存，返回不含密码的网络信息 |
| `DeleteProfile(ctx, id)` | 忘记网络 |
| `Connect(ctx, id)` / `Reconnect(ctx)` | 手动选择或重新连接 |
| `SetMode(ctx, mode)` | `ModeAuto`、`ModeCellular`、`ModeWiFi` |
| `RenewLease(ctx)` | 请求 DHCP 续租 |
| `TestDisconnect(ctx)` | 断线演练，暂停自动重连 15 秒，由正常监测回退 |

这些方法可被多个 goroutine 并发调用；网络操作与状态读取在内部排队，`Observe` 在就绪检查后独立执行只读采样，不阻塞网络操作队列。状态、列表、扫描结果为独立快照，调用方修改它们不会影响后台。请求取消后，尚未开始的操作会跳过；已经执行的操作可能完成，取消并不代表回滚。生命周期外调用返回 `ErrNotRunning`。

`Status.Traffic` 在观察到 WiFi 关联建立时提供一次收发计数基线（`RXBase` / `TXBase`）、接口编号、BSSID 和统计起点 `Since`。宿主可调用 `Observe(ctx)` 取得当前链路、计数与接口编号，并按同一接口相减，计算本次连接累计；关闭网页不会影响内核累计。模块不运行额外的流量采样定时器，也不把统计写盘。检测到断开、BSSID/SSID 变化或服务重启后重建基线；`Since` 是检测时间，初次检测前的少量流量可能漏计。读取失败时该字段缺省；接口不一致或计数回退时，宿主应显示不可用，不能继续沿用旧值。

## 配置与运行边界

- `Options.Tools` 可传入 iw、wpa_supplicant、BusyBox、ip、iptables 和 ip6tables 的绝对路径；空值使用 F50 Pro 默认路径。
- `Options.BaseDir` 是 root 私有目录，`RuntimeDir` 是 wifi 用户可访问的私有目录，两者必须分离，不能相互嵌套。
- 仍针对当前设备的 br0、relay_sta0 与表 30555；下游 IPv4 地址与掩码动态读取。同一设备只运行一个 Manager，不支持同时管理多个中继实例。
- 需要 Android/Linux、相应系统工具和网络管理权限；可在 macOS 构建/测试，不能在那里实际管理此设备。
- `ImportConfig(options, config)` 用于离线导入；`Recover(options)` 用于异常退出后的恢复。两者均获取同一把安装锁，拒绝修改正在运行的实例。
- 密码继续保存在 root 0600 的配置文件和 wifi 0600 的运行配置中。公开状态与日志不输出密码。

## DHCP 回调与 sh

仍调用 BusyBox udhcpc，并生成一小段 sh 回调。回调通过原子替换写入 root 私有租约事件文件，由 Manager 处理，不再反向执行当前主程序的 `lease` 子命令，也不依赖 HTTP/Unix 管理套接字。因此新的宿主程序不需要实现 relayd 的命令行协议。

每次启动 DHCP 客户端都会生成新的代号；回调带独立事件标识。这样旧客户端的迟到事件被拒绝，相同地址下的多次续租也会持续延长租期。sh 的职责仅为传递数据，认证、地址校验和出口切换由 Go 负责。

## 面板宿主（相邻 f50_pro_panel 项目）

- `cmd/f50panel`：当前统一入口，工作进程直接嵌入 Manager，同时提供独立面板、命令行和 Unix Socket。
- `internal/runner`：独立监管进程，负责异常清理和重新启动。
- 原版插件和第一版独立后台位于 `../ufitools_relay_plugin`，不包含在面板或本模块内。
- `internal/control`：把原有 JSON 操作映射为 Manager 方法，保持现有插件的请求与响应结构。
- 独立面板已经直接引用 Manager，无需启动 relayd 子进程或经过管理端口。

设备已切换到独立面板；旧 WiFi 插件与旧 relayd 程序已移除。

## 独立构建与分发

此目录拥有自己的 `go.mod`，只依赖 Go 标准库；不导入面板代码，也不包含 HTTP 登录、前端资源或启动脚本。内部文件写入工具随模块一起分发。

```sh
cd f50_pro_relay
go test -race ./...
go vet ./...
```

在本模块目录执行 `sh tools/build-package.sh`，生成 `build/f50_pro_relay-v0.3.1-source.tar.gz` 与 SHA-256 校验文件。解压后可以独立执行测试、构建和 `go doc`，无需整个面板项目。

本面板通过以下本地模块依赖使用它：

```go
require f50_pro_relay v0.3.1
replace f50_pro_relay => ../f50_pro_relay
```

其他本地项目也可以把 replace 指向解压目录。当前交付为独立源码发布包，尚未上传公共仓库。正式公开为可 `go get` 的模块时，需要使用实际仓库地址作为 module 路径、更新 import，并在该仓库打版本标签；当前不预设仓库所有者或许可证。

设备默认数据目录继续兼容此前安装路径 `/data/adb/ufi-relay` 和 `/data/vendor/wifi/ufi-relay`，以保留已保存网络；新宿主可以显式指定自己的 BaseDir 和 RuntimeDir。

## 按需观测接口（v0.4.0）

`Manager.Observe(ctx)` 只读取当前上游无线链路与流量计数，不扫描、不改网络、不创建后台轮询或历史文件。需要 Manager 正在运行；使用其 `Options.Tools.IW` 和包内部定义的中继接口，不要求宿主知道网卡名称或 sysfs 路径。

结果 `Observation` 包含采样时间、链路参数（RSSI、频段/信道、带宽、收发 PHY 速率与 MCS/NSS/GI/DCM）、当前网卡收发字节和接口编号。断开时 `Link.Connected=false`，计数缺省；链路查询失败返回 `ErrLinkUnavailable`，计数读取失败返回带已知链路的部分结果和 `ErrCountersUnavailable`。请求取消会终止查询子进程。错误可用 `errors.Is` 判断，显示文字由宿主负责。

`ReadObservation(ctx, options)` 用于诊断已有安装，不启动 Manager、不获取网络所有权，也不创建配置；面板并行验收时使用。它仍只是 Go 函数，没有 HTTP 或命令行入口。

中继虚拟客户端接口自 v0.4.1 起使用 `relay_sta0`（relay station）。所有连接、路由、探测和观测使用同一内部常量；panel 无需指定接口名称。网络所有权记录保存实际接口名，异常恢复兼容旧版 `ufi_sta0` 记录；升级时先停止旧实例并清理旧接口，再启动新版。热点接口仍为 `wlan0`。

## 动态下游网段（v0.5.0）

下游网络由 `ReadLAN()` 从实际 br0 地址与前缀读取；没有可用 IPv4 或同时存在多个候选时不猜测。启动、租约应用和现有检查周期均核实网络；地址、前缀或接口编号变化时先撤销旧策略/NAT/DNS 转发规则，退回蜂窝，再为新网段获取并应用上游租约。冲突检测使用当前下游 CIDR，支持包含关系，不再把 192.168.0.0/24 当成固定保留网段。

网络所有权文件保存 LAN 地址、CIDR 和接口，故异常清理按创建规则时的旧网段执行。旧版本未记录 LAN 的文件，仅在恢复清理时按其历史固定网段解释。连接跟踪清理也使用相应 LAN，保留下游本地管理会话。`Lease.Valid` 检查租约结构与有效期，实际路由应用还会检查当前 LAN 重叠。
