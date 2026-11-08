# Instabox for macOS

原生 macOS 桌面客户端，用 [MyGo](https://github.com/egoist/mygo)（纯 Go、零 CGO、Metal 渲染）开发，通过 **原生 RFB 3.8 协议**连接 [Instabox](https://instabox.sh) 云桌面（wayvnc），不是浏览器套壳——Omarchy/Hyprland 的 Super 组合键终于可以完整传递。

## 为什么不用浏览器 noVNC

浏览器里的 noVNC 会被网页截获 Cmd/Opt/Ctrl/Tab 等组合键，Omarchy 的 `Super+Return`（终端）、`Super+Space`（启动器）等快捷键全部失效。本客户端直接说 RFB 协议，键盘事件按物理键码（USB HID usage）映射为 X11 keysym，与字符无关——Option 改变字符、输入法组合等场景不会干扰快捷键判定。

## 功能

- 机器列表：状态（running / suspended / stopped）、冷却提示、启动 / 停止 / 暂停 / 恢复、连接 suspended 机器时自动唤醒
- 原生远程桌面：RFB 3.8 over WebSocket（None 认证，Instabox 中继即如此），支持 ZRLE / Hextile / RRE / CopyRect / Raw 编码、Cursor 伪编码（远程光标形状）、DesktopSize 伪编码、剪贴板双向同步（文本）
- 键盘：物理键码 → keysym 映射；修饰键映射可配置（默认 **Cmd→Super、Option→Alt、Control→Ctrl**）；自动重复；失焦 / 断开 / 退出捕获 / 退出全屏时自动补发全部 keyUp（防卡键）
- 键盘捕获模式（实验性）：CGEventTap 拦截 Cmd+Tab / Cmd+Space 等系统组合键发给远程；`Ctrl+Option+G` 或点击按钮退出；需要"辅助功能"权限
- **捕获输入模式**：在上述键盘捕获基础上隐藏本机光标，只显示远程光标（远端未给光标形状时画备用箭头）。远程光标的绘制与点击发送使用同一坐标映射，保证"指哪点哪"。窗口失焦自动释放并恢复本机光标
- **Cmd+W 转发**：桌面会话中按 Cmd+W 不会退出本应用，而是按修饰键映射（默认 Cmd→Super）发给远程，用于关闭远程系统里的软件；不在会话中时行为与 macOS 惯例一致（关闭窗口）
- 鼠标：移动 / 左右中键 / 滚轮 / 触控板精确滚动；缩放模式：适应窗口 / 填满 / 原始大小；Retina 自动处理
- 断线自动重连（指数退避 1s→15s），状态栏显示错误与"重连"按钮
- API Key 存 macOS 钥匙串（`INSTABOX_API_KEY` 环境变量优先，可覆盖钥匙串），绝不写入磁盘源码或日志

## 安装与构建

需要 Go 1.27.3+。

```bash
git clone https://github.com/cypggs/instabox-mac.git
cd instabox-mac
go tool mygo build        # 产出 dist/darwin-arm64/Instabox.app 与 instaboxmac-<版本>-arm64.dmg
```

开发运行：

```bash
INSTABOX_API_KEY=ibk_... go run .
```

`INSTABOX_AUTOCONNECT=1` 可让应用启动后自动连接第一台 running/suspended 机器（调试用）。

Intel：MyGo 支持 `darwin/universal` 交叉编译，但本项目只在 Apple Silicon 上实测过。

## 配置

首次启动点右上角"设置"粘贴 API Key（`ibk_` 开头，从 dashboard.instabox.sh 生成），保存进钥匙串后重启应用生效。同一面板可修改修饰键映射。

## 键盘捕获模式与权限

点工具栏"捕获键盘"开启。首次会弹出 macOS"辅助功能"授权请求——本应用**只用它拦截键盘事件转发给远程桌面**，不做任何其他用途。授权后如未生效请重启应用。

**如实告知的限制（macOS 安全设计，非本应用能绕过）：**

- 无法捕获"安全事件"：密码输入框、锁屏、**Cmd+Opt+Esc 强制退出对话框**中的按键不会到达捕获层（这是 macOS 的硬性安全边界，我们也不应该、不能够绕过它——强制退出始终是逃生通道）
- 我们无法承诺所有 macOS 系统快捷键都能被捕获；捕获能力以实测为准（Cmd+W 已在菜单层转发给远程，其余系统键建议用捕获输入模式）
- 退出捕获三保险：`Ctrl+Option+G`（物理键，与映射配置无关）、工具栏按钮、菜单"会话 → 返回机器列表"
- 窗口失焦会自动退出捕获、恢复本机光标并释放全部按键（避免你在别的应用打字时被吞键）

## 测试

```bash
# 单元测试（协议编解码、键映射、修饰键状态机，全部离线）
go test ./...

# 真实 Instabox 集成测试（握手 + 帧接收 + 按键/指针发送路径 + 唤醒路径）
INSTABOX_API_KEY=ibk_... go test ./internal/integration/ -integration -v -timeout 180s
```

没有 `INSTABOX_API_KEY` 时集成测试自动跳过并明确报告"未验证"——不伪造测试结果。

## 项目结构

```
main.go                  # 应用入口：窗口、菜单、捕获模式装配
internal/
  instabox/              # Instabox HTTP API 客户端（X-API-Key、幂等键、错误码）
  rfb/                   # RFB 3.8 客户端（纯 Go）：握手、编码解码、帧缓冲
  keys/                  # HID 键码 → X11 keysym 全表 + 修饰键映射状态机
  grab/                  # CGEventTap 键盘捕获（purego，零 CGO）
  appui/                 # MyGo 原生 UI：机器列表、桌面视图、会话状态机、偏好
  integration/           # 真实实例集成测试（-integration flag）
```

### 实现备注

- **ZRLE 流兼容**：wayvnc/neatvnc 对整条连接使用一条 zlib 流、每矩形 `Z_SYNC_FLUSH` 而不 `Z_FINISH`（TigerVNC/RealVNC 则是每矩形独立流）。`internal/rfb/zrle.go` 的 `zrleState` 两种模式都支持：先按持久流累积解码，检测到独立流特征自动回退。历史累积超过 8MB 时报错触发自动重连以控制内存。
- **像素格式协商为 bpp32/depth24 小端**，wire 像素 `[R,G,B,X]`，解码零交换；ZRLE 的 CPIXEL 取低 3 字节。
- **平铺更新请求**：帧缓冲被切成 64x64 小块网格。连接后全部小块非增量请求（首帧），之后由扫描循环轮流增量请求。原因：实测 Instabox 中继会按连接随机吞掉过大的服务器→客户端消息（同一尺寸时通时断，且断连后无错误提示），小块请求把单条更新消息压到 16KB 以内，配合首帧看门狗（12s 无画面自动重连）规避该故障模式。
- **首帧看门狗**：握手成功只是"中继接受了连接"，不保证数据真的流转（故障模式下表现为永远收不到画面）。12 秒未收到首帧判定为中继故障，自动断线重连（每次重连都是一次新的中继骰子）。

## 已知限制 / 未验证项

- **Instabox 中继的按连接随机故障**：同一客户端、同一请求，不同连接的表现可能完全不同（握手后永远无数据 / 大消息被吞 / 完全正常），已排除客户端与 wayvnc 因素（机器内直连 wayvnc 100% 正常）。客户端已用小轮询块 + 首帧看门狗 + 自动重连规避：遇到坏连接会在十几秒内自动换新连接重试，通常 1-2 次内成功。若长时间只有空白画面，查看状态栏错误信息。
- **交互输入（真实键鼠 → 远程）与 Omarchy 快捷键逐项验收**：协议层发送路径已由集成测试覆盖，但"本地 UI 事件 → 远程效果"的端到端链路需要真实用户环境手测（自动化合成事件在本机被 macOS 拦截）。请在 Omarchy 机器上按验收清单自测：Cmd+Return、Cmd+Space、Cmd+Shift+B/W、Cmd+1-3、Cmd+方向键、Opt+Tab、Ctrl+Opt+Return。
- 剪贴板走 RFB 基础 CutText（Latin-1），非 Latin-1 字符（如中文）会被替换为 `?`。ExtendedClipboard（UTF-8）协商未实现。
- 捕获模式对 Cmd+Space 的实际拦截效果以实测为准。
- 8MB ZRLE 历史上限触发时会断开重连一次（内存与解码速度的折中）。

## 安全

- API Key 只存 macOS 钥匙串（或环境变量），不出现在源码、日志、git 历史中
- 不禁用 macOS 任何安全逃生机制（强制退出、安全输入照常工作）
- 不修改远程 Omarchy 的默认快捷键配置

## License

MIT
