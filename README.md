# 全能远程管理工具

## 项目介绍
这是一款基于 Go 语言开发的跨平台远程管理工具，集成 Windows RDP 远程桌面和 Linux SSH 远程连接功能，核心特性包括：
- RDP 连接支持多监视器（multimon）自定义开启/关闭
- SSH 连接内置 `trzsz` 协议，文件传输（`trz`/`tsz`）自动弹窗，无需手动配置
- 优化的交互逻辑：高频功能置顶、列表优先+搜索后置，贴合运维使用习惯
- 自动进程管理、异常处理，无冗余报错，适配生产环境使用

## 环境支持平台
### 操作系统支持
| 系统类型       | 版本/发行版                | 支持状态 | 备注                     |
|----------------|----------------------------|----------|--------------------------|
| Linux          | Debian 10+/Ubuntu 18.04+   | ✅ 完全支持 | 优先适配 Debian 13       |
| Linux          | CentOS 7+/Rocky Linux 8+   | ✅ 完全支持 | 需要手动安装依赖         |
| macOS          | macOS 12+ (Intel/Apple Silicon) | ✅ 基本支持 | RDP 依赖需手动安装       |
| Windows        | Windows 10/11/Server 2019+ | ⚠️ 有限支持 | 需 WSL2 环境运行 SSH 功能 |

### 依赖要求
| 功能模块 | 必需依赖                | 安装命令（Debian/Ubuntu）|
|----------|-------------------------|-------------------------------------------|
| RDP      | `xfreerdp3`             | `sudo apt install -y freerdp3-x11`        |
| SSH      | `trzsz` + 终端模拟器    | `sudo apt install -y trzsz gnome-terminal`|
| SSH 密码登录 | `sshpass`              | `sudo apt install -y sshpass`             |

> 注意：`gnome-terminal` 为默认终端，也可替换为 `xfce4-terminal`/`xterm`/`mlterm` 等，工具会自动检测。

## 高效编译成二进制文件
### 前置条件
1. 安装 Go 环境（推荐 1.20+ 版本）：
   ```bash
   # Debian/Ubuntu 安装 Go
   sudo apt install -y golang-go
   # 验证安装
   go version # 输出 go1.20+ 即正常
   ```
2. 克隆/下载代码到本地，进入代码目录：
   ```bash
   cd /path/to/remote-manager
   ```

### 本地快速编译（当前平台）
```bash
# 精简编译（去除调试信息，减小体积）
go build -ldflags="-s -w" -o remote-manager main.go

# 赋予执行权限
chmod +x remote-manager
```

### 跨平台交叉编译
支持编译为其他系统/架构的二进制文件，无需对应环境：

| 目标平台       | 编译命令                                                                 |
|----------------|--------------------------------------------------------------------------|
| Linux amd64    | `GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o remote-manager-linux-amd64 main.go` |
| Linux arm64    | `GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o remote-manager-linux-arm64 main.go` |
| macOS amd64    | `GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o remote-manager-darwin-amd64 main.go` |
| macOS arm64    | `GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o remote-manager-darwin-arm64 main.go` |

> 编译优化说明：
> - `-ldflags="-s -w"`：去除符号表和调试信息，二进制体积可减少 30%+
> - 交叉编译无需安装对应平台依赖，直接生成可执行文件

### 编译后验证
```bash
# 检查二进制文件可用性
./remote-manager --help # 正常进入交互界面即编译成功
# 检查文件大小（精简后约 8-10MB）
ls -lh remote-manager
```

## 快速使用指南
### 1. 启动工具
```bash
./remote-manager
```

### 2. 核心功能操作
#### 主菜单选择
```
=====================================================
🚀 全能远程管理工具 [RDP+SSH+✔️trzsz传输+多监视器+无报错] ✨
=====================================================
1. Windows 远程管理 (RDP)
2. Linux   远程管理 (SSH)
q. 退出程序
请选择管理类型 [1/2/q]: 
```

#### RDP 远程桌面（Windows）
1. 选择 `1` 进入 RDP 子菜单：
   ```
   =====================================================
   🚀 Windows 远程桌面(RDP) 管理子菜单
   =====================================================
   1. 连接主机
   2. 列出所有主机
   3. 添加主机
   4. 编辑主机
   5. 删除主机
   6. 断开连接
   b. 返回上级菜单
   请选择操作 [1-6/b]: 
   ```
2. 选择 `1. 连接主机` → 选择目标主机 → 选择多监视器功能：
   ```
   🖥️  多监视器功能设置 (multimon)
   1. 开启 (添加 /multimon:force 参数，使用多个显示器)
   2. 不开启 (不添加该参数)
   请选择 [1/2] (默认 2): 
   ```
3. 输入 `1` 开启多监视器，`2` 或回车不开启，自动启动 RDP 窗口。

#### SSH 远程连接（Linux）
1. 选择 `2` 进入 SSH 子菜单（同 RDP 子菜单结构）；
2. 选择 `1. 连接主机` → 选择目标主机，自动启动终端并建立 SSH 连接；
3. 文件传输命令（连接后在终端执行）：
   - 下载文件：`trz 文件名`（自动弹窗选择本地保存路径）
   - 上传文件：`tsz 文件名`（自动弹窗选择本地文件）

### 3. 主机管理
- **添加主机**：选择 `3. 添加主机`，按提示输入 IP、端口、用户名、密码等信息；
- **列出主机**：选择 `2. 列出所有主机`，默认展示全部主机列表，支持关键词搜索（主机名/IP/用户名）；
- **编辑/删除主机**：选择 `4/5`，按序号选择目标主机操作。

## 配置文件说明
- 配置文件路径：`./config.yaml`（工具首次启动自动生成）；
- 存储内容：主机列表、RDP 模板参数；
- 安全提示：密码以明文存储，建议仅在可信环境使用，或使用 SSH 密钥登录替代密码。

## 常见问题解决

### 1. RDP 多监视器不生效
- 检查：确保 Windows 端开启“允许远程连接”，且本地有多个显示器；
- 验证：手动执行 `xfreerdp3 /u:用户名 /p:密码 /v:IP:端口 /multimon:force` 测试。

### 2. trzsz 无弹窗
- 检查：确保安装 `trzsz`（`sudo apt install trzsz`）；
- 验证：终端执行 `trzsz version` 输出版本信息即正常。

### 3. 编译报错：`package gopkg.in/yaml.v3: unrecognized import path`
- 解决：下载依赖后再编译：
  ```bash
  go mod tidy
  go build -ldflags="-s -w" -o remote-manager main.go
  ```

## 性能优化建议
1. **二进制瘦身**：编译后可使用 `upx` 压缩（体积再减 50%+）：
   ```bash
   sudo apt install -y upx
   upx -9 remote-manager
   ```
2. **后台运行**：使用 `nohup` 后台运行，避免终端关闭中断：
   ```bash
   nohup ./remote-manager > /dev/null 2>&1 &
   ```
3. **批量部署**：将编译后的二进制文件复制到多台服务器，无需重复编译：
   ```bash
   scp remote-manager user@server-ip:/usr/local/bin/
   ```

## 核心特性总结
| 特性                | 优势                                     |
|---------------------|------------------------------------------|
| 多监视器自定义      | RDP 连接按需开启多屏，适配多显示器场景   |
| trzsz 自动集成      | SSH 文件传输无需手动配置，弹窗操作更友好 |
| 交互逻辑优化        | 高频功能置顶、列表优先，运维操作更高效   |
| 跨平台编译          | 一份代码编译多平台二进制，部署成本低     |
| 进程自动管理        | 异常退出自动清理进程，无残留连接         |

## 免责声明
- 本工具仅用于合法合规的远程管理场景，禁止用于未经授权的访问；
- 使用者需遵守所在国家/地区的法律法规，开发者不承担任何滥用导致的责任。
