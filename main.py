#!/usr/bin/env python3
# -*- coding: utf-8 -*-
import os
import sys
import yaml
import time
import psutil
import signal
import shlex
import socket
import getpass
import subprocess
import threading
from dataclasses import dataclass, field
from typing import List, Dict, Optional, Any, Callable, Tuple
from pathlib import Path

# ===================== 核心常量定义 =====================
DEFAULT_RDP_PORT: int = 3389
DEFAULT_SSH_PORT: int = 22
MAX_PORT: int = 65535
CONNECT_TIMEOUT: int = 10
MAX_RETRY_COUNT: int = 2
RECENT_CONN_MAX_COUNT: int = 10

# 命令配置
XFREERDP_CMD: str = "xfreerdp3"
SSH_CMD: str = "ssh"
TRZSZ_CMD: str = "trzsz"
SSHPASS_CMD: str = "sshpass"
CONFIG_FILE: str = "config.yaml"

# 主机类型
HOST_TYPE_RDP: str = "rdp"
HOST_TYPE_SSH: str = "ssh"
DEFAULT_HOST_NAME: str = "Remote-Host"

# 显示器模式常量
DISPLAY_MODE_SINGLE: str = "single"
DISPLAY_MODE_MULTI: str = "multi"

# ===================== 数据结构定义 =====================
@dataclass
class RDPProfile:
    """RDP配置模板"""
    name: str
    desc: str = ""
    args: List[str] = field(default_factory=list)

@dataclass
class Host:
    """主机配置（明文存储密码）"""
    name: str
    ip: str
    port: int
    username: str
    password: str
    drive: str = ""
    type: str = HOST_TYPE_RDP
    key_path: str = ""
    rdp_profile: str = ""
    last_conn: str = ""

@dataclass
class Config:
    """全局配置"""
    rdp_profiles: List[RDPProfile] = field(default_factory=list)
    hosts: List[Host] = field(default_factory=list)
    recent_conns: List[str] = field(default_factory=list)

# ===================== 全局变量 =====================
# 确保配置文件路径是绝对路径，避免工作目录问题
config_path: Path = Path(os.path.abspath(__file__)).parent / CONFIG_FILE
global_config: Config = Config()  # 初始化不为None
active_sessions: Dict[str, int] = {}
session_lock = threading.Lock()

# ===================== 高阶函数：通用装饰器 =====================
def validate_port(func: Callable) -> Callable:
    """装饰器：校验端口合法性"""
    def wrapper(port: int, host_type: str, *args, **kwargs):
        if not isinstance(port, int) or port <= 0 or port > MAX_PORT:
            default_port = DEFAULT_SSH_PORT if host_type == HOST_TYPE_SSH else DEFAULT_RDP_PORT
            print(f"⚠️ 端口{port}无效，使用默认端口{default_port}")
            port = default_port
        return func(port, host_type, *args, **kwargs)
    return wrapper

def handle_exceptions(default_return: Any = None) -> Callable:
    """装饰器：通用异常处理"""
    def decorator(func: Callable) -> Callable:
        def wrapper(*args, **kwargs):
            try:
                return func(*args, **kwargs)
            except ValueError as e:
                print(f"❌ 参数错误: {e}")
            except subprocess.CalledProcessError as e:
                print(f"❌ 命令执行失败: {e.stderr.decode() if e.stderr else str(e)}")
        return wrapper
    return decorator

# ===================== 高阶函数：命令构建器（核心修改） =====================
def command_builder(cmd_type: str) -> Callable:
    """闭包：构建RDP/SSH命令（高阶函数）"""
    def build_rdp_command(host: Host, display_mode: str = DISPLAY_MODE_SINGLE) -> List[str]:
        """
        构建正确的xfreerdp3命令（支持显示器模式选择）
        :param host: 主机配置
        :param display_mode: 显示器模式 single/multi
        """
        # 核心参数（严格遵循xfreerdp3语法）
        cmd = [XFREERDP_CMD]
        cmd.append(f"/v:{host.ip}:{get_real_port(host.port, HOST_TYPE_RDP)}")
        cmd.append(f"/u:{host.username}")
        cmd.append(f"/p:{host.password}")

        # 基础参数（修复后的正确格式）
        base_args = [
            "/cert:ignore", "/f", "/bpp:32", "/dynamic-resolution",
            "/auto-reconnect"
        ]
        cmd.extend(base_args)

        # 驱动器映射（严格遵循/drive:name,path格式）
        drive_path = expand_path(host.drive) or expand_path("~")
        has_drives_off = False

        # 检查模板是否禁用驱动器
        if host.rdp_profile and global_config.rdp_profiles:
            for profile in global_config.rdp_profiles:
                if profile.name == host.rdp_profile:
                    has_drives_off = "/drives-off" in profile.args
                    break

        # 未禁用则添加驱动器映射（xfreerdp3正确格式）
        if not has_drives_off:
            cmd.append(f"/drive:local,{drive_path}")

        # 添加模板扩展参数（过滤掉模板中的/multimon参数，由用户选择决定）
        profile_args = []
        if host.rdp_profile and global_config.rdp_profiles:
            for profile in global_config.rdp_profiles:
                if profile.name == host.rdp_profile:
                    # 过滤掉模板中的multimon参数，避免冲突
                    profile_args = [arg for arg in profile.args if arg != "/multimon"]
                    break
        
        cmd.extend(profile_args)

        # 根据用户选择添加多显示器参数
        if display_mode == DISPLAY_MODE_MULTI:
            cmd.append("/multimon")
            print("🖥️ 已启用多显示器模式")
        else:
            print("🖥️ 已启用单显示器模式")

        return cmd

    def build_ssh_command(host: Host) -> List[str]:
        """构建SSH命令（带trzsz）"""
        port = get_real_port(host.port, HOST_TYPE_SSH)
        ssh_cmd = [SSH_CMD, "-p", str(port), "-l", host.username]
        ssh_cmd.extend(["-o", "StrictHostKeyChecking=no", "-o", f"ConnectTimeout={CONNECT_TIMEOUT}"])

        # 密钥登录
        key_path = expand_path(host.key_path)
        if key_path and os.path.exists(key_path):
            ssh_cmd.insert(1, "-i")
            ssh_cmd.insert(2, key_path)

        ssh_cmd.append(host.ip)

        # 添加trzsz
        full_cmd = [TRZSZ_CMD] + ssh_cmd

        # 密码登录（使用sshpass）
        if host.password and not key_path:
            full_cmd = [SSHPASS_CMD, "-p", host.password] + full_cmd

        return full_cmd

    if cmd_type == HOST_TYPE_RDP:
        # 返回带显示器模式参数的函数
        return build_rdp_command
    elif cmd_type == HOST_TYPE_SSH:
        return build_ssh_command
    else:
        raise ValueError(f"不支持的命令类型: {cmd_type}")

# ===================== 高阶函数：命令执行器（适配修改） =====================
def command_executor(session_type: str) -> Callable:
    """高阶函数：执行命令并管理会话"""
    @handle_exceptions(default_return=False)
    def execute(host: Host, display_mode: str = DISPLAY_MODE_SINGLE) -> bool:
        """
        执行命令并跟踪会话（适配RDP显示器模式）
        :param host: 主机配置
        :param display_mode: 显示器模式 single/multi
        """
        # 获取命令构建器
        build_cmd = command_builder(session_type)
        
        # 根据会话类型传递不同参数
        if session_type == HOST_TYPE_RDP:
            cmd_args = build_cmd(host, display_mode)
        else:
            cmd_args = build_cmd(host)

        # 打印格式化的命令（便于调试）
        print(f"🔧 执行命令: {shlex.join(cmd_args)}")

        # 启动子进程
        proc = subprocess.Popen(
            cmd_args,
            stdin=sys.stdin,
            stdout=sys.stdout,
            stderr=sys.stderr,
            preexec_fn=os.setsid
        )

        # 记录会话
        session_key = get_host_key(host)
        with session_lock:
            active_sessions[session_key] = proc.pid

        # 后台监控进程
        def monitor():
            proc.wait()
            with session_lock:
                if session_key in active_sessions:
                    del active_sessions[session_key]

        threading.Thread(target=monitor, daemon=True).start()
        add_recent_conn(session_key)
        print(f"✅ {session_type.upper()}连接成功！PID: {proc.pid}")
        return True

    return execute

# ===================== 核心工具函数 =====================
@validate_port
def get_real_port(port: int, host_type: str) -> int:
    """获取有效端口（带装饰器校验）"""
    return port

def get_host_key(host: Host) -> str:
    """生成唯一主机标识"""
    host_type = host.type or HOST_TYPE_RDP
    return f"[{host_type}]{host.name}|{host.ip}:{get_real_port(host.port, host_type)}"

def is_command_exist(cmd: str) -> bool:
    """检查命令是否存在"""
    try:
        subprocess.run(["which", cmd], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        return True
    except subprocess.CalledProcessError:
        return False

def expand_path(path: str) -> str:
    """扩展路径"""
    return os.path.expanduser(path) if path else ""

def read_input(prompt: str) -> str:
    """读取用户输入"""
    try:
        return input(prompt).strip()
    except (EOFError, KeyboardInterrupt):
        return ""

def read_password(prompt: str) -> str:
    """读取密码"""
    try:
        return getpass.getpass(prompt)
    except (EOFError, KeyboardInterrupt):
        print("\n⚠️ 无法隐藏输入，将明文显示")
        return read_input(prompt)

def select_display_mode() -> str:
    """
    让用户选择显示器模式
    :return: single/multi
    """
    print("\n🖥️ 选择显示器模式：")
    print("1. 单显示器模式（默认）")
    print("2. 多显示器模式")
    
    while True:
        choice = read_input("请选择（1/2，回车默认1）：")
        if not choice:
            return DISPLAY_MODE_SINGLE
        elif choice == "1":
            return DISPLAY_MODE_SINGLE
        elif choice == "2":
            return DISPLAY_MODE_MULTI
        else:
            print("❌ 无效选择，请输入1或2")

@handle_exceptions(default_return=(0.0, False))
def test_connectivity(ip: str, port: int) -> Tuple[float, bool]:
    """测试网络连通性"""
    start_time = time.time()
    with socket.create_connection((ip, port), timeout=CONNECT_TIMEOUT):
        delay = (time.time() - start_time) * 1000
        return delay, True
    return 0.0, False

def get_terminal_cmd() -> str:
    """获取可用终端"""
    for cmd in ["gnome-terminal", "xfce4-terminal", "xterm", "mlterm"]:
        if is_command_exist(cmd):
            return cmd
    return ""

def build_terminal_args(term_cmd: str, title: str, cmd_str: str) -> List[str]:
    """构建终端参数"""
    if term_cmd == "gnome-terminal":
        return ["--title", title, "--wait", "--", "bash", "-c", cmd_str]
    elif term_cmd == "xfce4-terminal":
        return ["--title", title, "-H", "-x", "bash", "-c", cmd_str]
    else:
        return ["-T", title, "-e", cmd_str]

# ===================== 配置管理 =====================
@handle_exceptions(default_return=Config())
def load_config(force_reload: bool = False) -> Config:
    """
    加载配置文件（核心优化）
    :param force_reload: 是否强制重新加载（忽略缓存）
    """
    global global_config
    
    # 如果配置文件不存在，返回空配置
    if not config_path.exists():
        print(f"⚠️ 配置文件不存在: {config_path}")
        return Config()

    try:
        with open(config_path, "r", encoding="utf-8") as f:
            data = yaml.safe_load(f) or {}
        
        # 解析RDP模板
        rdp_profiles = []
        for p in data.get("rdp_profiles", []):
            # 兼容手动修改的配置，确保字段存在
            profile = RDPProfile(
                name=p.get("name", ""),
                desc=p.get("desc", ""),
                args=p.get("args", [])
            )
            rdp_profiles.append(profile)
        
        # 解析主机配置
        hosts = []
        for h in data.get("hosts", []):
            # 兼容手动修改的配置，设置默认值
            host = Host(
                name=h.get("name", DEFAULT_HOST_NAME),
                ip=h.get("ip", ""),
                port=h.get("port", DEFAULT_RDP_PORT if h.get("type") == HOST_TYPE_RDP else DEFAULT_SSH_PORT),
                username=h.get("username", ""),
                password=h.get("password", ""),
                drive=h.get("drive", ""),
                type=h.get("type", HOST_TYPE_RDP),
                key_path=h.get("key_path", ""),
                rdp_profile=h.get("rdp_profile", ""),
                last_conn=h.get("last_conn", "")
            )
            hosts.append(host)
        
        # 解析最近连接
        recent_conns = data.get("recent_conns", [])
        
        # 创建配置对象
        new_config = Config(
            rdp_profiles=rdp_profiles,
            hosts=hosts,
            recent_conns=recent_conns
        )
        
        # 如果强制重载，更新全局配置
        if force_reload:
            global_config = new_config
            print(f"✅ 已从 {config_path} 重新加载配置")
        
        return new_config
    
    except Exception as e:
        print(f"❌ 加载配置失败: {e}")
        # 加载失败时返回当前全局配置，避免程序崩溃
        return global_config

@handle_exceptions(default_return=False)
def save_config(config: Config) -> bool:
    """保存配置文件（优化写入逻辑）"""
    try:
        # 确保目录存在
        config_path.parent.mkdir(parents=True, exist_ok=True)
        
        # 转换为可序列化的字典
        data = {
            "rdp_profiles": [
                {
                    "name": p.name,
                    "desc": p.desc,
                    "args": p.args
                } for p in config.rdp_profiles
            ],
            "hosts": [
                {
                    "name": h.name,
                    "ip": h.ip,
                    "port": h.port,
                    "username": h.username,
                    "password": h.password,
                    "drive": h.drive,
                    "type": h.type,
                    "key_path": h.key_path,
                    "rdp_profile": h.rdp_profile,
                    "last_conn": h.last_conn
                } for h in config.hosts
            ],
            "recent_conns": config.recent_conns
        }
        
        # 写入文件（使用safe_dump确保编码正确）
        with open(config_path, "w", encoding="utf-8") as f:
            yaml.safe_dump(
                data, 
                f, 
                indent=2, 
                encoding="utf-8",
                allow_unicode=True,  # 确保中文字符正常保存
                sort_keys=False       # 保持字段顺序，便于手动编辑
            )
        
        # 写入后立即刷新全局配置
        global global_config
        global_config = config
        
        print(f"✅ 配置已保存到: {config_path}")
        return True
        
    except Exception as e:
        print(f"❌ 保存配置失败: {e}")
        return False

def reload_config() -> None:
    """手动重新加载配置（供菜单调用）"""
    global global_config
    global_config = load_config(force_reload=True)
    print("✅ 配置已重新加载完成")

def init_default_config() -> None:
    """初始化默认配置"""
    if config_path.exists():
        return

    # 默认RDP模板（修复xfreerdp3参数）
    default_profiles = [
        RDPProfile(
            name="基础模式",
            desc="核心功能，稳定连接",
            args=["/sound:sys:pulse", "/clipboard", "/drives"]
        ),
        RDPProfile(
            name="高性能模式",
            desc="多显示器+USB+音频（连接时可选择是否启用多显示器）",
            args=["/sound:sys:pulse", "/microphone:sys:pulse", "/usb:auto", "/clipboard", "/drives"]
        ),
        RDPProfile(
            name="极简模式",
            desc="仅基础桌面（修复禁用参数）",
            args=["/sound:0", "/drives-off", "/clipboard-off"]  # 正确的禁用参数
        )
    ]

    config = Config(rdp_profiles=default_profiles, hosts=[], recent_conns=[])
    save_config(config)
    print("✅ 默认配置已创建")

def add_recent_conn(host_key: str) -> None:
    """添加最近连接记录"""
    if not isinstance(global_config, Config):
        return

    # 去重并保持最新
    new_conns = [host_key] + [k for k in global_config.recent_conns if k != host_key]
    global_config.recent_conns = new_conns[:RECENT_CONN_MAX_COUNT]
    # 保存时确保全局配置同步
    save_config(global_config)

# ===================== 连接管理核心功能（核心修改） =====================
def connect_rdp(host: Host) -> None:
    """连接RDP主机（增加显示器模式选择）"""
    # 前置检查
    if not host.username or not host.password:
        print("❌ 用户名/密码不能为空")
        return

    if not is_command_exist(XFREERDP_CMD):
        print(f"❌ 未安装{XFREERDP_CMD}，请执行：sudo apt install freerdp3-x11")
        return

    # 让用户选择显示器模式
    display_mode = select_display_mode()

    # 获取执行器并执行（传递显示器模式参数）
    rdp_executor = command_executor(HOST_TYPE_RDP)

    # 重试机制
    success = False
    for i in range(MAX_RETRY_COUNT):
        if rdp_executor(host, display_mode):
            success = True
            break
        print(f"⚠️ 连接失败（重试{i+1}/{MAX_RETRY_COUNT}）")
        time.sleep(1)

    if not success:
        print("❌ RDP连接失败")

def connect_ssh(host: Host) -> None:
    """连接SSH主机（使用高阶函数执行器）"""
    # 前置检查
    if not is_command_exist(SSH_CMD) or not is_command_exist(TRZSZ_CMD):
        print("❌ 缺少依赖，请执行：sudo apt install openssh-client trzsz")
        return

    # 连通性测试
    port = get_real_port(host.port, HOST_TYPE_SSH)
    delay, ok = test_connectivity(host.ip, port)
    status = "✅ 可达" if ok else "❌ 不可达"
    print(f"🔍 连通性测试: {host.ip}:{port} - {status} (延迟: {delay:.1f}ms)")

    # 获取终端
    term_cmd = get_terminal_cmd()
    if not term_cmd:
        print("❌ 未检测到终端，请安装gnome-terminal/xfce4-terminal")
        return

    # 构建命令
    build_cmd = command_builder(HOST_TYPE_SSH)
    cmd_args = build_cmd(host)
    cmd_str = shlex.join(cmd_args) + "; read -n1 -p '按任意键退出...'"

    # 构建终端命令
    title = f"SSH-{host.name}({host.ip}:{port})"
    term_args = build_terminal_args(term_cmd, title, cmd_str)
    final_cmd = [term_cmd] + term_args

    # 执行命令
    try:
        proc = subprocess.Popen(final_cmd, preexec_fn=os.setsid)
        session_key = get_host_key(host)
        with session_lock:
            active_sessions[session_key] = proc.pid

        # 监控进程
        def monitor_ssh():
            proc.wait()
            with session_lock:
                if session_key in active_sessions:
                    del active_sessions[session_key]

        threading.Thread(target=monitor_ssh, daemon=True).start()
        add_recent_conn(session_key)
        print(f"✅ SSH连接成功！PID: {proc.pid}")
    except Exception as e:
        print(f"❌ SSH连接失败: {e}")

# ===================== 主机管理功能 =====================
def filter_hosts(host_type: str) -> List[Host]:
    """过滤指定类型主机（每次调用前刷新配置）"""
    # 每次过滤前重新加载配置，确保获取最新数据
    load_config(force_reload=True)
    return [h for h in global_config.hosts if h.type == host_type]

def show_host_list(hosts: List[Host], host_type: str) -> List[Host]:
    """显示主机列表并支持搜索"""
    if not hosts:
        print(f"📭 暂无{host_type}类型主机")
        return []

    # 显示列表
    print(f"\n📋 {host_type.upper()}主机列表（共{len(hosts)}台）")
    print("序号 | 名称 | 地址 | 用户名 | 备注")
    print("----------------------------------")

    for i, host in enumerate(hosts, 1):
        addr = f"{host.ip}:{get_real_port(host.port, host_type)}"
        note = host.drive if host_type == HOST_TYPE_RDP else (host.key_path or "密码登录")
        print(f"{i:<4} | {host.name:<4} | {addr:<8} | {host.username:<6} | {note}")

    # 搜索过滤
    keyword = read_input("\n🔍 搜索关键词（回车跳过）：")
    if not keyword:
        return hosts

    lower_key = keyword.lower()
    return [h for h in hosts if lower_key in h.name.lower() or lower_key in h.ip.lower()]

def add_host(host_type: str) -> None:
    """添加主机"""
    # 基础信息
    name = read_input("主机名称：")
    ip = read_input("IP/域名：")
    username = read_input("用户名：")

    if not all([name, ip, username]):
        print("❌ 名称/IP/用户名不能为空")
        return

    # 端口
    port_str = read_input(f"端口（默认{DEFAULT_RDP_PORT if host_type == HOST_TYPE_RDP else DEFAULT_SSH_PORT}）：")
    port = DEFAULT_RDP_PORT if host_type == HOST_TYPE_RDP else DEFAULT_SSH_PORT
    if port_str:
        try:
            port = int(port_str)
        except ValueError:
            print(f"⚠️ 端口无效，使用默认{port}")

    # 密码
    password = read_password("密码（隐藏输入）：")

    # 创建主机
    host = Host(
        name=name, ip=ip, port=port, username=username,
        password=password, type=host_type
    )

    # 扩展信息
    if host_type == HOST_TYPE_RDP:
        host.drive = expand_path(read_input("共享路径（默认~）：") or "~")

        # 选择RDP模板
        if global_config.rdp_profiles:
            print("\n📋 RDP模板列表：")
            for i, profile in enumerate(global_config.rdp_profiles, 1):
                print(f"{i}. {profile.name} - {profile.desc}")

            idx_str = read_input("选择模板序号（回车跳过）：")
            if idx_str:
                try:
                    idx = int(idx_str) - 1
                    if 0 <= idx < len(global_config.rdp_profiles):
                        host.rdp_profile = global_config.rdp_profiles[idx].name
                except ValueError:
                    print("⚠️ 无效序号，未选择模板")
    else:
        host.key_path = expand_path(read_input("密钥路径（回车为空）："))

    # 添加到全局配置并保存
    global_config.hosts.append(host)
    if save_config(global_config):
        print("✅ 主机添加成功")
        # 添加后立即重新加载配置，确保数据同步
        load_config(force_reload=True)
    else:
        print("❌ 添加失败")

def edit_host(host_type: str) -> None:
    """编辑主机"""
    hosts = filter_hosts(host_type)
    if not hosts:
        return

    filtered = show_host_list(hosts, host_type)
    idx_str = read_input("编辑序号：")

    try:
        idx = int(idx_str) - 1
        if not (0 <= idx < len(filtered)):
            print("❌ 序号无效")
            return
    except ValueError:
        print("❌ 序号无效")
        return

    # 查找原主机
    target_host = filtered[idx]
    original_idx = next((i for i, h in enumerate(global_config.hosts) if get_host_key(h) == get_host_key(target_host)), None)

    if original_idx is None:
        print("❌ 主机不存在")
        return

    # 编辑信息
    host = global_config.hosts[original_idx]

    new_name = read_input(f"新名称（当前：{host.name}）：")
    new_ip = read_input(f"新IP（当前：{host.ip}）：")
    new_password = read_password("新密码（回车不变）：")

    if new_name: host.name = new_name
    if new_ip: host.ip = new_ip
    if new_password: host.password = new_password

    # 扩展信息
    if host_type == HOST_TYPE_RDP:
        new_drive = expand_path(read_input(f"新共享路径（当前：{host.drive}）："))
        if new_drive: host.drive = new_drive
    else:
        new_key = expand_path(read_input(f"新密钥路径（当前：{host.key_path}）："))
        if new_key: host.key_path = new_key

    # 保存并重新加载
    if save_config(global_config):
        load_config(force_reload=True)
        print("✅ 主机编辑成功")
    else:
        print("❌ 编辑失败")

def delete_host(host_type: str) -> None:
    """删除主机"""
    hosts = filter_hosts(host_type)
    if not hosts:
        return

    filtered = show_host_list(hosts, host_type)
    idx_str = read_input("删除序号：")

    try:
        idx = int(idx_str) - 1
        if not (0 <= idx < len(filtered)):
            print("❌ 序号无效")
            return
    except ValueError:
        print("❌ 序号无效")
        return

    host = filtered[idx]
    if read_input(f"确认删除{host.name}？(y/N)：").lower() != "y":
        print("✅ 取消删除")
        return

    # 删除
    target_key = get_host_key(host)
    global_config.hosts = [h for h in global_config.hosts if get_host_key(h) != target_key]

    # 保存并重新加载
    if save_config(global_config):
        load_config(force_reload=True)
        print("✅ 主机删除成功")
    else:
        print("❌ 删除失败")

def connect_host(host_type: str) -> None:
    """连接主机"""
    hosts = filter_hosts(host_type)
    if not hosts:
        return

    filtered = show_host_list(hosts, host_type)
    idx_str = read_input("连接序号：")

    try:
        idx = int(idx_str) - 1
        if not (0 <= idx < len(filtered)):
            print("❌ 序号无效")
            return
    except ValueError:
        print("❌ 序号无效")
        return

    host = filtered[idx]
    if host_type == HOST_TYPE_RDP:
        connect_rdp(host)
    else:
        connect_ssh(host)

def disconnect_host() -> None:
    """断开连接"""
    with session_lock:
        if not active_sessions:
            print("📭 无活跃连接")
            return

        # 显示活跃会话
        print("\n🔌 活跃连接列表：")
        print("序号 | 连接信息 | PID")
        print("---------------------")
        keys = list(active_sessions.keys())
        for i, key in enumerate(keys, 1):
            print(f"{i:<4} | {key:<8} | {active_sessions[key]}")

        # 选择断开
        idx_str = read_input("断开序号：")
        try:
            idx = int(idx_str) - 1
            if not (0 <= idx < len(keys)):
                print("❌ 序号无效")
                return
        except ValueError:
            print("❌ 序号无效")
            return

        # 终止进程
        key = keys[idx]
        pid = active_sessions[key]
        try:
            proc = psutil.Process(pid)
            proc.terminate()
            proc.wait(timeout=2)
            del active_sessions[key]
            print("✅ 连接已断开")
        except (psutil.NoSuchProcess, psutil.TimeoutExpired):
            del active_sessions[key]
            print("✅ 连接已断开（进程已结束）")

def show_recent_conns() -> None:
    """显示最近连接（先刷新配置）"""
    # 显示前重新加载配置
    load_config(force_reload=True)
    
    if not global_config.recent_conns:
        print("📭 无最近连接记录")
        return

    # 显示记录
    print("\n📝 最近连接记录：")
    print("序号 | 连接信息")
    print("------------")
    for i, key in enumerate(global_config.recent_conns, 1):
        print(f"{i:<4} | {key}")

    # 快速连接
    idx_str = read_input("快速连接序号（回车跳过）：")
    if not idx_str:
        return

    try:
        idx = int(idx_str) - 1
        if not (0 <= idx < len(global_config.recent_conns)):
            print("❌ 序号无效")
            return
    except ValueError:
        print("❌ 序号无效")
        return

    # 查找主机并连接
    target_key = global_config.recent_conns[idx]
    for host in global_config.hosts:
        if get_host_key(host) == target_key:
            if host.type == HOST_TYPE_RDP:
                connect_rdp(host)
            else:
                connect_ssh(host)
            return

    print("❌ 主机不存在")

def batch_test(host_type: str) -> None:
    """批量测试连通性（先刷新配置）"""
    # 测试前重新加载配置
    load_config(force_reload=True)
    
    hosts = filter_hosts(host_type)
    if not hosts:
        return

    print(f"\n🚀 批量测试{host_type.upper()}主机连通性：")
    print("名称 | 地址 | 延迟 | 状态")
    print("------------------------")

    for host in hosts:
        port = get_real_port(host.port, host_type)
        delay, ok = test_connectivity(host.ip, port)
        status = "✅ 可达" if ok else "❌ 不可达"
        delay_str = f"{delay:.1f}ms" if ok else "-"
        print(f"{host.name} | {host.ip}:{port} | {delay_str} | {status}")

# ===================== 菜单系统 =====================
def show_sub_menu(host_type: str) -> None:
    """显示子菜单"""
    title = "Windows RDP" if host_type == HOST_TYPE_RDP else "Linux SSH"

    while True:
        print(f"\n===== {title} 管理 =====")
        print("1. 连接主机")
        print("2. 添加主机")
        print("3. 编辑主机")
        print("4. 删除主机")
        print("5. 断开连接")
        print("6. 批量测试连通性")
        print("7. 最近连接")
        print("8. 重新加载配置")  # 新增：手动重新加载配置
        print("b. 返回主菜单")

        choice = read_input("选择操作：")

        if choice == "1":
            connect_host(host_type)
        elif choice == "2":
            add_host(host_type)
        elif choice == "3":
            edit_host(host_type)
        elif choice == "4":
            delete_host(host_type)
        elif choice == "5":
            disconnect_host()
        elif choice == "6":
            batch_test(host_type)
        elif choice == "7":
            show_recent_conns()
        elif choice == "8":  # 新增：手动重载配置
            reload_config()
        elif choice.lower() == "b":
            return
        else:
            print("❌ 无效选项")

def show_main_menu() -> None:
    """显示主菜单"""
    while True:
        print("\n===== 全能远程管理工具 (Python 3.12) =====")
        print("1. Windows 远程桌面 (RDP)")
        print("2. Linux 远程终端 (SSH)")
        print("3. 最近连接记录")
        print("4. 重新加载配置")  # 新增：主菜单也可重载配置
        print("q. 退出程序")

        choice = read_input("选择功能：")

        if choice == "1":
            show_sub_menu(HOST_TYPE_RDP)
        elif choice == "2":
            show_sub_menu(HOST_TYPE_SSH)
        elif choice == "3":
            show_recent_conns()
        elif choice == "4":  # 新增：主菜单重载配置
            reload_config()
        elif choice.lower() == "q":
            print("👋 感谢使用，再见！")
            sys.exit(0)
        else:
            print("❌ 无效选项")

# ===================== 信号处理 =====================
def signal_handler(sig, frame):
    """处理退出信号"""
    print("\n🛑 正在关闭所有连接...")
    with session_lock:
        for pid in active_sessions.values():
            try:
                proc = psutil.Process(pid)
                proc.terminate()
                proc.wait(timeout=2)
            except (psutil.NoSuchProcess, psutil.TimeoutExpired):
                pass
    print("👋 程序已退出")
    sys.exit(0)

# ===================== 主函数 =====================
def main() -> None:
    global global_config
    """主函数"""
    # 注册信号处理
    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)

    # 初始化配置
    init_default_config()

    # 加载配置（强制重载）
    load_config(force_reload=True)

    # 启动菜单
    print("🚀 全能远程管理工具（Python 3.12版）")
    print("🔧 已修复xfreerdp3参数格式 | 🛡️ 高阶函数重构 | 📝 配置持久化 | 🔄 动态重载配置 | 🖥️ 显示器模式选择")
    show_main_menu()

if __name__ == "__main__":
    main()