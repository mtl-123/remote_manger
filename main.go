package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
	"golang.org/x/term"
)

// ===================== 常量定义 =====================
const (
	DefaultRDPPort  = 3389
	DefaultSSHPort  = 22
	MaxPort         = 65535
	XfreerdpCmd     = "xfreerdp3"
	SSHCmd          = "ssh"
	TrzszCmd        = "trzsz"
	SshpassCmd      = "sshpass"
	ConfigFileName  = "config.yaml"
	DirPermission   = 0700
	FilePermission  = 0600
	DefaultHostName = "RDP-Host"
	DefaultSSHName  = "SSH-Host"
	HostTypeRDP     = "rdp"
	HostTypeSSH     = "ssh"
)

// RDP 功能模板
type RDPProfile struct {
	Name string   `yaml:"name"`
	Desc string   `yaml:"desc,omitempty"`
	Args []string `yaml:"args"`
}

// Host 核心结构体
type Host struct {
	Name       string `yaml:"name"`
	IP         string `yaml:"ip"`
	Port       int    `yaml:"port"`
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`
	Drive      string `yaml:"drive"`
	Type       string `yaml:"type"`
	KeyPath    string `yaml:"key_path"`
	RDPProfile string `yaml:"rdp_profile"`
}

// Config 整体配置
type Config struct {
	RDPProfiles []RDPProfile `yaml:"rdp_profiles,omitempty"`
	Hosts       []Host       `yaml:"hosts"`
}

// 全局变量
var (
	configPath     string
	activeSessions = make(map[string]int)
	sessionsMutex  sync.Mutex
	globalCfg      *Config
)

func init() {
	exePath, err := os.Executable()
	if err != nil {
		exePath = os.Args[0]
	}
	exeDir := filepath.Dir(exePath)
	configPath = filepath.Join(exeDir, ConfigFileName)

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-c
		fmt.Println("\n\n🛑 收到退出信号，正在优雅退出...")
		sessionsMutex.Lock()
		for key, pid := range activeSessions {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Kill()
				fmt.Printf("✅ 关闭残留连接: %s (PID: %d)\n", key, pid)
			}
		}
		sessionsMutex.Unlock()
		fmt.Println("👋 再见！")
		os.Exit(0)
	}()
}

// ===================== 核心工具函数 =====================
func GetRealPort(port int, hostType string) int {
	if port <= 0 || port > MaxPort {
		if hostType == HostTypeSSH {
			return DefaultSSHPort
		}
		return DefaultRDPPort
	}
	return port
}

func GetAddr(ip string, port int, hostType string) string {
	return ip + ":" + strconv.Itoa(GetRealPort(port, hostType))
}

func IsValidAddr(addr string) bool {
	if addr == "" {
		return false
	}
	if net.ParseIP(addr) != nil {
		return true
	}
	_, err := net.LookupIP(addr)
	return err == nil
}

func IsDirExist(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func IsFileExist(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func ExpandPath(path string) string {
	if path == "" || !strings.HasPrefix(path, "~") {
		return path
	}
	home := getHomeDir()
	return filepath.Join(home, path[1:])
}

func IsProcessAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err != syscall.ESRCH
}

func CleanDeadSessions() {
	sessionsMutex.Lock()
	defer sessionsMutex.Unlock()

	deadKeys := make([]string, 0)
	for key, pid := range activeSessions {
		if !IsProcessAlive(pid) {
			deadKeys = append(deadKeys, key)
		}
	}

	for _, key := range deadKeys {
		delete(activeSessions, key)
	}
}

func IsCommandExist(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

func getEffectiveHostType(h Host) string {
	if h.Type == "" {
		return HostTypeRDP
	}
	return h.Type
}

func hostKey(h Host) string {
	hostType := getEffectiveHostType(h)
	rawPort := h.Port
	if rawPort <= 0 || rawPort > MaxPort {
		rawPort = GetRealPort(rawPort, hostType)
	}
	return fmt.Sprintf("[%s]%s|%s:%d", hostType, h.Name, h.IP, rawPort)
}

func getHomeDir() string {
	if usr, err := user.Current(); err == nil {
		return usr.HomeDir
	}
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	return "/tmp"
}

func readInput(prompt string) string {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return ""
	}
	return strings.TrimSpace(input)
}

func readPassword(prompt string) string {
	fmt.Print(prompt)
	bytePassword, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Println("\n⚠️ 无法隐藏输入，将明文显示密码")
		return readInput("")
	}
	fmt.Println()
	return string(bytePassword)
}

func startCmdAndTrack(cmd *exec.Cmd, sessionKey string) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动进程失败: %w", err)
	}
	go func() {
		_ = cmd.Wait()
		sessionsMutex.Lock()
		delete(activeSessions, sessionKey)
		sessionsMutex.Unlock()
	}()
	sessionsMutex.Lock()
	activeSessions[sessionKey] = cmd.Process.Pid
	sessionsMutex.Unlock()
	return nil
}

// ===================== 智能模糊搜索过滤主机 =====================
func searchFilterHosts(allHosts []Host, keyword string) []Host {
	var filtered []Host
	if keyword == "" {
		return allHosts
	}
	lowerKeyword := strings.ToLower(keyword)
	for _, host := range allHosts {
		matchContent := strings.ToLower(host.Name + " " + host.IP + " " + host.Username)
		if strings.Contains(matchContent, lowerKeyword) {
			filtered = append(filtered, host)
		}
	}
	return filtered
}

// ===================== 列表优先，搜索后置 =====================
func showHostListWithSearchOpt(hosts []Host, hostType string) []Host {
	if len(hosts) == 0 {
		hostName := "Windows(RDP)"
		if hostType == HostTypeSSH {
			hostName = "Linux(SSH)"
		}
		fmt.Printf("📭 当前无任何【%s】主机配置。\n", hostName)
		return nil
	}

	// 先直接展示所有主机列表
	hostName := "Windows(RDP)"
	colTitle := "共享路径"
	if hostType == HostTypeSSH {
		hostName = "Linux(SSH)"
		colTitle = "密钥路径"
	}

	fmt.Printf("\n📋 所有【%s】主机列表 (共 %d 台):\n", hostName, len(hosts))
	fmt.Println("序号 | 名称             | 地址                | 用户名        | " + colTitle)
	fmt.Println("----------------------------------------------------------------------")
	for i, h := range hosts {
		addr := GetAddr(h.IP, h.Port, hostType)
		displayName := h.Name
		if displayName == "" {
			if hostType == HostTypeSSH {
				displayName = DefaultSSHName
			} else {
				displayName = DefaultHostName
			}
		}
		extInfo := h.Drive
		if hostType == HostTypeSSH {
			extInfo = h.KeyPath
			if extInfo == "" {
				extInfo = "(密码登录)"
			}
		}
		fmt.Printf("%-4d | %-16s | %-19s | %-12s | %s\n", i+1, displayName, addr, h.Username, extInfo)
	}

	// 按需搜索
	keyword := readInput("\n🔍 输入关键词搜索(主机名/IP/用户名，回车不搜索直接使用): ")
	filteredHosts := searchFilterHosts(hosts, keyword)

	if len(filteredHosts) == 0 && keyword != "" {
		fmt.Println("😕 未找到匹配的主机，将返回全部列表！")
		return hosts
	}
	if len(filteredHosts) > 0 && keyword != "" {
		fmt.Printf("✅ 筛选出 %d 台匹配的主机！\n", len(filteredHosts))
	}
	return filteredHosts
}

// ===================== 配置文件管理 =====================
func ensureConfigExists() error {
	dir := filepath.Dir(configPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, DirPermission); err != nil {
			return fmt.Errorf("无法创建配置目录: %v", err)
		}
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Println("未找到配置文件，正在创建默认配置...")
		defaultProfiles := []RDPProfile{
			{
				Name: "高性能办公",
				Desc: "含音频、视频、多显示器、驱动器、剪贴板等",
				Args: []string{
					"+aero", "+async-channels", "+async-update", "+auto-reconnect",
					"/auto-reconnect-max-retries:5", "/cert:ignore", "+disp",
					"/dynamic-resolution", "+home-drive", "/timeout:5000", "+video",
					"+window-drag", "+clipboard", "/video", "+jpeg", "+echo", "+f",
					"/network:auto", "/bpp:32", "/microphone:sys:pulse",
					"/sound:sys:pulse,latency:100", "/rfx", "/usb:auto", "+drives",
					"+fonts", "+wallpaper", "+themes", "+menu-anims", "-compression",
				},
			},
			{
				Name: "基础桌面",
				Desc: "仅剪贴板和驱动器",
				Args: []string{"+clipboard", "+drives"},
			},
			{
				Name: "安全最小化",
				Desc: "仅图形，禁用所有重定向",
				Args: []string{"/cert:ignore"},
			},
		}
		cfg := &Config{
			RDPProfiles: defaultProfiles,
			Hosts:       []Host{},
		}
		if err := saveConfig(cfg); err != nil {
			return err
		}
		fmt.Printf("✅ 配置文件已创建: %s\n", configPath)
	}
	return nil
}

func loadConfig() (*Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("配置文件格式错误: %v", err)
	}
	return &cfg, nil
}

func saveConfig(cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, FilePermission)
}

func filterHosts(cfg *Config, hostType string) []Host {
	var filtered []Host
	for _, h := range cfg.Hosts {
		if getEffectiveHostType(h) == hostType {
			filtered = append(filtered, h)
		}
	}
	return filtered
}

func selectRDPProfile(cfg *Config) string {
	if len(cfg.RDPProfiles) == 0 {
		fmt.Println("⚠️ 无可用 RDP 模板，将使用默认参数。")
		return ""
	}

	fmt.Println("\n请选择 RDP 功能模板：")
	for i, p := range cfg.RDPProfiles {
		desc := p.Desc
		if desc == "" {
			desc = "无描述"
		}
		fmt.Printf("%d. %s → %s\n", i+1, p.Name, desc)
	}
	fmt.Printf("%d. 自定义（高级用户）\n", len(cfg.RDPProfiles)+1)

	choiceStr := readInput(fmt.Sprintf("请输入序号 [1-%d]: ", len(cfg.RDPProfiles)+1))
	choice, err := strconv.Atoi(choiceStr)
	if err != nil || choice < 1 || choice > len(cfg.RDPProfiles)+1 {
		fmt.Println("❌ 无效选择，使用默认模板。")
		return ""
	}

	if choice <= len(cfg.RDPProfiles) {
		return cfg.RDPProfiles[choice-1].Name
	}

	fmt.Println("请输入 xfreerdp3 参数（空格分隔，如 '+clipboard +drives'）:")
	custom := readInput("自定义参数: ")
	if custom == "" {
		return ""
	}
	name := readInput("为此自定义模板命名（用于后续复用）: ")
	if name == "" {
		name = "自定义-" + strconv.FormatInt(time.Now().Unix(), 10)
	}
	args := strings.Fields(custom)
	cfg.RDPProfiles = append(cfg.RDPProfiles, RDPProfile{
		Name: name,
		Desc: "用户自定义模板",
		Args: args,
	})
	if err := saveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "保存自定义模板失败: %v\n", err)
	}
	return name
}

func addNewHost(cfg *Config, hostType string) {
	var name string
	for {
		name = readInput("主机名称（不可为空）: ")
		if name != "" {
			break
		}
		fmt.Println("⚠️ 主机名称不能为空，请重新输入。")
	}

	var ip string
	for {
		ip = readInput("IP/域名: ")
		if IsValidAddr(ip) {
			break
		}
		fmt.Println("⚠️ IP/域名格式无效，请输入合法的IPv4/IPv6地址或域名。")
	}

	defaultPort := DefaultRDPPort
	portTip := "3389"
	if hostType == HostTypeSSH {
		defaultPort = DefaultSSHPort
		portTip = "22"
	}
	portStr := readInput(fmt.Sprintf("端口号（回车默认 %s）: ", portTip))
	port := defaultPort
	if portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 && p < MaxPort {
			port = p
		} else {
			fmt.Printf("⚠️ 端口无效，使用默认 %s\n", portTip)
		}
	}

	tempHost := Host{Name: name, IP: ip, Port: port, Type: hostType}
	for _, h := range cfg.Hosts {
		if hostKey(h) == hostKey(tempHost) {
			fmt.Println("⚠️ 该主机（类型+名称+IP:端口）已存在，无需重复添加。")
			return
		}
	}

	username := readInput("用户名: ")
	password := readPassword("密码（隐藏输入，SSH密钥登录可留空）: ")

	if hostType == HostTypeRDP && password == "" {
		fmt.Println("⚠️ RDP 连接必须提供密码！确定要留空吗？(y/N)")
		if readInput("") != "y" {
			fmt.Println("添加已取消。")
			return
		}
	}

	var ext1 string
	var rdpProfile string
	if hostType == HostTypeRDP {
		ext1 = readInput("本地共享路径（回车默认 家目录）: ")
		if ext1 == "" {
			ext1 = getHomeDir()
		}
		ext1 = ExpandPath(ext1)
		if !IsDirExist(ext1) {
			fmt.Printf("⚠️ 路径 %s 不存在或不是目录，仍要使用吗？(y/N): ", ext1)
			if readInput("") != "y" {
				fmt.Println("添加已取消。")
				return
			}
		}
		rdpProfile = selectRDPProfile(cfg)
	} else {
		ext1 = readInput("密钥文件路径（回车则密码登录，例：~/.ssh/id_rsa）: ")
		ext1 = ExpandPath(ext1)
		if ext1 != "" && !IsFileExist(ext1) {
			fmt.Printf("⚠️ 密钥文件 %s 不存在，仍要使用吗？(y/N): ", ext1)
			if readInput("") != "y" {
				fmt.Println("添加已取消。")
				return
			}
		}
	}

	fmt.Println("⚠️ 温馨提示：密码将以明文形式存储在配置文件中，密钥登录更安全！")

	newHost := Host{
		Name:       name,
		IP:         ip,
		Port:       port,
		Username:   username,
		Password:   password,
		Type:       hostType,
		RDPProfile: rdpProfile,
	}
	if hostType == HostTypeRDP {
		newHost.Drive = ext1
	} else {
		newHost.KeyPath = ext1
	}

	cfg.Hosts = append(cfg.Hosts, newHost)
	if err := saveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "保存失败: %v\n", err)
		return
	}
	fmt.Println("✅ 主机添加成功！")
}

func editExistHost(cfg *Config, hostType string) {
	hosts := filterHosts(cfg, hostType)
	if len(hosts) == 0 {
		return
	}
	showHostListWithSearchOpt(hosts, hostType)

	idxStr := readInput("请输入要编辑的主机序号: ")
	var idx int
	_, err := fmt.Sscanf(idxStr, "%d", &idx)
	if err != nil || idx < 1 || idx > len(hosts) {
		fmt.Println("❌ 无效序号。")
		return
	}

	var realIdx int
	for i, h := range cfg.Hosts {
		if hostKey(h) == hostKey(hosts[idx-1]) {
			realIdx = i
			break
		}
	}
	h := &cfg.Hosts[realIdx]

	var newName string
	for {
		newName = readInput(fmt.Sprintf("新名称（当前: %s，回车跳过，不可为空）: ", h.Name))
		if newName == "" {
			newName = h.Name
		}
		if newName != "" {
			h.Name = newName
			break
		}
		fmt.Println("⚠️ 名称不能为空，请输入。")
	}

	if newIP := readInput("新 IP/域名（回车跳过）: "); newIP != "" {
		if IsValidAddr(newIP) {
			h.IP = newIP
		} else {
			fmt.Println("⚠️ IP/域名格式无效，保持原地址不变。")
		}
	}

	currentPort := GetRealPort(h.Port, hostType)
	if newPortStr := readInput(fmt.Sprintf("新端口（当前 %d，回车跳过）: ", currentPort)); newPortStr != "" {
		if p, err := strconv.Atoi(newPortStr); err == nil && p > 0 && p < MaxPort {
			h.Port = p
		} else {
			fmt.Println("⚠️ 端口无效，保持不变")
		}
	}

	if newUser := readInput("新用户名（回车跳过）: "); newUser != "" {
		h.Username = newUser
	}

	if readInput("是否修改密码？(y/N): ") == "y" {
		h.Password = readPassword("新密码（隐藏输入）: ")
		if hostType == HostTypeRDP && h.Password == "" {
			fmt.Println("⚠️ RDP 密码为空！确定保存吗？(y/N)")
			if readInput("") != "y" {
				fmt.Println("密码未更新。")
			}
		}
		fmt.Println("⚠️ 温馨提示：密码将以明文形式存储在配置文件中！")
	}

	if hostType == HostTypeRDP {
		if newDrive := readInput("新共享路径（回车跳过）: "); newDrive != "" {
			newDrive = ExpandPath(newDrive)
			if !IsDirExist(newDrive) {
				fmt.Printf("⚠️ 路径 %s 不存在或不是目录，仍要使用吗？(y/N): ", newDrive)
				if readInput("") != "y" {
					fmt.Println("路径未更新。")
				} else {
					h.Drive = newDrive
				}
			} else {
				h.Drive = newDrive
			}
		}
		if readInput("是否修改 RDP 功能模板？(y/N): ") == "y" {
			h.RDPProfile = selectRDPProfile(cfg)
		}
	} else {
		if newKey := readInput("新密钥路径（回车跳过，留空则密码登录）: "); newKey != "" {
			newKey = ExpandPath(newKey)
			if !IsFileExist(newKey) {
				fmt.Printf("⚠️ 密钥文件 %s 不存在，仍要使用吗？(y/N): ", newKey)
				if readInput("") != "y" {
					fmt.Println("密钥路径未更新。")
				} else {
					h.KeyPath = newKey
				}
			} else {
				h.KeyPath = newKey
			}
		}
	}

	if err := saveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "保存失败: %v\n", err)
		return
	}
	fmt.Println("✅ 主机更新成功！")
}

func delExistHost(cfg *Config, hostType string) {
	hosts := filterHosts(cfg, hostType)
	if len(hosts) == 0 {
		return
	}
	filteredHosts := showHostListWithSearchOpt(hosts, hostType)
	if filteredHosts == nil {
		return
	}

	idxStr := readInput("请输入要删除的主机序号: ")
	var idx int
	_, err := fmt.Sscanf(idxStr, "%d", &idx)
	if err != nil || idx < 1 || idx > len(filteredHosts) {
		fmt.Println("❌ 无效序号。")
		return
	}

	confirm := readInput(fmt.Sprintf("⚠️ 确认要删除序号 %d 的主机吗？(y/N): ", idx))
	if confirm != "y" && confirm != "Y" {
		fmt.Println("✅ 删除操作已取消。")
		return
	}

	var newHosts []Host
	targetKey := hostKey(filteredHosts[idx-1])
	for _, h := range cfg.Hosts {
		if hostKey(h) != targetKey {
			newHosts = append(newHosts, h)
		}
	}
	cfg.Hosts = newHosts

	if err := saveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "保存失败: %v\n", err)
		return
	}
	fmt.Println("✅ 主机已删除。")
}

// ===================== ✅ 核心新增：RDP多监视器选择功能 =====================
func connectRDPHost(h Host, cfg *Config) {
	if h.Name == "" {
		h.Name = DefaultHostName
	}

	drivePath := ExpandPath(h.Drive)
	if drivePath == "" {
		drivePath = getHomeDir()
	}
	if !IsDirExist(drivePath) {
		fmt.Printf("❌ 共享路径不存在或不是目录: %s\n", drivePath)
		fmt.Println("请先编辑主机修正路径。")
		return
	}

	addr := GetAddr(h.IP, h.Port, HostTypeRDP)

	if !IsCommandExist(XfreerdpCmd) {
		fmt.Println("❌ 未检测到 xfreerdp3，请先安装：sudo apt install xfreerdp3")
		return
	}

	// ✅ 新增：询问是否开启多监视器功能
	fmt.Println("\n🖥️  多监视器功能设置 (multimon)")
	fmt.Println("1. 开启 (添加 /multimon:force 参数，使用多个显示器)")
	fmt.Println("2. 不开启 (不添加该参数)")
	multimonChoice := readInput("请选择 [1/2] (默认 2): ")
	var multimonArg string
	switch multimonChoice {
	case "1":
		multimonArg = "/multimon:force"
		fmt.Println("✅ 已选择开启多监视器功能")
	default:
		multimonArg = ""
		fmt.Println("✅ 已选择不开启多监视器功能")
	}

	fmt.Printf("🚀 准备RDP连接: %s (%s)\n", h.Name, addr)
	fmt.Printf("   • 用户名: %s\n", h.Username)
	if h.Password == "" {
		fmt.Println("   ⚠️ 警告: 密码为空！连接将失败。")
	}
	if h.RDPProfile != "" {
		fmt.Printf("   • RDP模板: %s\n", h.RDPProfile)
	} else {
		fmt.Println("   • RDP模板: 默认参数")
	}
	fmt.Println("ℹ️ 启动独立窗口...（若窗口闪退，请检查凭据、网络或防火墙）")

	cmdArgs := []string{
		"/u:" + h.Username,
		"/p:" + h.Password,
		"/v:" + addr,
		"/t:" + h.Name,
		"/drive:local," + drivePath,
	}

	// ✅ 新增：如果选择开启多监视器，添加对应的参数
	if multimonArg != "" {
		cmdArgs = append(cmdArgs, multimonArg)
	}

	var extraArgs []string
	if h.RDPProfile != "" {
		for _, p := range cfg.RDPProfiles {
			if p.Name == h.RDPProfile {
				extraArgs = p.Args
				break
			}
		}
	}

	if len(extraArgs) == 0 {
		extraArgs = []string{
			"+clipboard",
			"/sound:sys:pulse",
			"/cert:ignore",
			"+f",
		}
	}

	cmdArgs = append(cmdArgs, extraArgs...)

	cmd := exec.Command(XfreerdpCmd, cmdArgs...)

	cleanEnv := os.Environ()
	proxyEnvList := []string{"http_proxy", "https_proxy", "all_proxy", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "no_proxy", "NO_PROXY"}
	newEnv := make([]string, 0, len(cleanEnv))
envFilter:
	for _, env := range cleanEnv {
		for _, proxyEnv := range proxyEnvList {
			if strings.HasPrefix(env, proxyEnv+"=") {
				continue envFilter
			}
		}
		newEnv = append(newEnv, env)
	}
	cmd.Env = newEnv

	sessionKey := hostKey(h)
	if err := startCmdAndTrack(cmd, sessionKey); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		return
	}

	fmt.Printf("✅ 已启动RDP独立窗口: %s (%s) [PID %d]\n", h.Name, addr, cmd.Process.Pid)
	fmt.Println("💡 提示：可通过「6. 断开连接」终止，或直接关闭RDP窗口。")
}

// ===================== SSH连接（修复spawn pty failed报错） =====================
func connectSSHHost(h Host) {
	if h.Name == "" {
		h.Name = DefaultSSHName
	}

	realPort := GetRealPort(h.Port, HostTypeSSH)
	hostAddr := fmt.Sprintf("%s:%d", h.IP, realPort)

	// 自动检测终端，优先gnome-terminal
	var termCmd string
	termCmds := []string{"gnome-terminal", "xfce4-terminal", "xterm", "mlterm", "terminator"}
	for _, cmd := range termCmds {
		if IsCommandExist(cmd) {
			termCmd = cmd
			break
		}
	}
	if termCmd == "" {
		fmt.Println("❌ 未检测到终端软件，推荐安装：sudo apt install gnome-terminal")
		return
	}

	// 强制启用trzsz
	if !IsCommandExist(TrzszCmd) {
		fmt.Println("❌ 未检测到 trzsz 工具，请安装：sudo apt install trzsz")
		return
	}
	fmt.Println("✅ ✔️ 已启用 trzsz 协议连接 → trz/tsz 文件传输命令必弹窗生效")
	fmt.Println("📁 文件传输命令(连接后直接输入)：【上传文件】tsz 文件名  【下载文件】trz")

	// 修复参数拼接顺序
	sshCommandArgs := []string{
		"-p", strconv.Itoa(realPort),
		"-l", h.Username,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=quiet",
		h.IP,
	}

	// 密钥登录：追加-i参数
	if h.KeyPath != "" && IsFileExist(ExpandPath(h.KeyPath)) {
		keyPath := ExpandPath(h.KeyPath)
		sshCommandArgs = append([]string{"-i", keyPath}, sshCommandArgs...)
	}

	// 组装最终命令
	var finalCmd []string
	hasPassword := h.Password != ""
	useKey := h.KeyPath != "" && IsFileExist(ExpandPath(h.KeyPath))

	if useKey {
		finalCmd = append([]string{TrzszCmd, SSHCmd}, sshCommandArgs...)
		fmt.Printf("🔑 正在连接: %s [%s] (密钥登录 + trzsz文件传输)\n", h.Name, hostAddr)
	} else if hasPassword {
		if !IsCommandExist(SshpassCmd) {
			fmt.Printf("\n❌ 缺少 sshpass 依赖，请安装：sudo apt install sshpass\n")
			return
		}
		finalCmd = append([]string{SshpassCmd, "-p", h.Password, TrzszCmd, SSHCmd}, sshCommandArgs...)
		fmt.Printf("🔐 正在连接: %s [%s] (密码登录 + trzsz文件传输)\n", h.Name, hostAddr)
	} else {
		finalCmd = append([]string{TrzszCmd, SSHCmd}, sshCommandArgs...)
		fmt.Printf("👤 正在连接: %s [%s] (手动输密码 + trzsz文件传输)\n", h.Name, hostAddr)
	}

	// 终端参数拼接
	var termArgs []string
	cmdStr := strings.Join(finalCmd, " ") + "; read -n1 -p '连接断开，按任意键关闭窗口...'"
	switch termCmd {
	case "gnome-terminal":
		termArgs = []string{"--title", fmt.Sprintf("SSH-%s(%s) ✔️trzsz传输", h.Name, hostAddr), "--", "bash", "-c", cmdStr}
	case "xfce4-terminal":
		termArgs = []string{"--title", fmt.Sprintf("SSH-%s(%s) ✔️trzsz传输", h.Name, hostAddr), "-x", "bash", "-c", cmdStr}
	case "xterm", "mlterm", "terminator":
		termArgs = []string{"-T", fmt.Sprintf("SSH-%s(%s) ✔️trzsz传输", h.Name, hostAddr), "-e", cmdStr}
	}

	// 执行命令
	cmd := exec.Command(termCmd, termArgs...)
	sessionKey := hostKey(h)
	if err := startCmdAndTrack(cmd, sessionKey); err != nil {
		fmt.Fprintf(os.Stderr, "❌ 启动SSH窗口失败: %v\n", err)
		return
	}

	fmt.Printf("✅ 连接成功！PID: %d → 输入命令立即弹窗传输文件\n", cmd.Process.Pid)
	fmt.Println("💡 提示：可通过「6. 断开连接」终止，或直接关闭终端窗口。")
}

// ===================== 统一连接入口 =====================
func doConnect(cfg *Config, hostType string) {
	hosts := filterHosts(cfg, hostType)
	if len(hosts) == 0 {
		return
	}

	filteredHosts := showHostListWithSearchOpt(hosts, hostType)
	if filteredHosts == nil {
		return
	}

	idxStr := readInput("请输入要连接的主机序号: ")
	var idx int
	_, err := fmt.Sscanf(idxStr, "%d", &idx)
	if err != nil || idx < 1 || idx > len(filteredHosts) {
		fmt.Println("❌ 无效序号，请重试。")
		return
	}

	if hostType == HostTypeRDP {
		connectRDPHost(filteredHosts[idx-1], cfg)
	} else {
		connectSSHHost(filteredHosts[idx-1])
	}
}

func disconnectHost() {
	CleanDeadSessions()
	sessionsMutex.Lock()
	defer sessionsMutex.Unlock()

	if len(activeSessions) == 0 {
		fmt.Println("📭 当前无活跃连接。")
		return
	}

	fmt.Println("\n🔌 所有活跃远程连接 (自动过滤已断开会话):")
	fmt.Println("序号 | 连接信息                          | 进程PID")
	fmt.Println("-----------------------------------------------------------")
	keys := make([]string, 0, len(activeSessions))
	for k := range activeSessions {
		keys = append(keys, k)
	}
	for i, key := range keys {
		fmt.Printf("%-4d | %-35s | %d\n", i+1, key, activeSessions[key])
	}

	idxStr := readInput("请输入要断开的连接序号: ")
	var idx int
	_, err := fmt.Sscanf(idxStr, "%d", &idx)
	if err != nil || idx < 1 || idx > len(keys) {
		fmt.Println("❌ 无效序号。")
		return
	}

	selectedKey := keys[idx-1]
	pid := activeSessions[selectedKey]

	confirm := readInput(fmt.Sprintf("⚠️ 确认要断开 [%s] (PID:%d) 吗？(y/N): ", selectedKey, pid))
	if confirm != "y" && confirm != "Y" {
		fmt.Println("✅ 断开操作已取消。")
		return
	}

	proc, err := os.FindProcess(pid)
	if err == nil {
		_ = proc.Kill()
	}

	delete(activeSessions, selectedKey)
	fmt.Printf("✅ 已断开连接: %s (PID %d)\n", selectedKey, pid)
}

// ===================== 子菜单（高频功能置顶） =====================
func showSubMenu(cfg *Config, hostType string) {
	hostTypeName := "Windows 远程桌面(RDP)"
	if hostType == HostTypeSSH {
		hostTypeName = "Linux 远程终端(SSH) ✔️trzsz文件传输必弹窗"
	}

	for {
		fmt.Println("\n=====================================================")
		fmt.Printf("🚀 %s 管理子菜单\n", hostTypeName)
		fmt.Println("=====================================================")
		fmt.Println("1. 连接主机          【高频常用，置顶优先】")
		fmt.Println("2. 列出所有主机      【先展示全部，再按需搜索】")
		fmt.Println("3. 添加主机")
		fmt.Println("4. 编辑主机")
		fmt.Println("5. 删除主机")
		fmt.Println("6. 断开连接")
		fmt.Println("b. 返回上级菜单")
		choice := readInput("请选择操作 [1-6/b]: ")

		switch choice {
		case "1":
			doConnect(cfg, hostType)
		case "2":
			showHostListWithSearchOpt(filterHosts(cfg, hostType), hostType)
		case "3":
			addNewHost(cfg, hostType)
		case "4":
			editExistHost(cfg, hostType)
		case "5":
			delExistHost(cfg, hostType)
		case "6":
			disconnectHost()
		case "b", "B":
			fmt.Println("🔙 返回上级菜单...")
			return
		default:
			fmt.Println("❌ 无效选项，请重试。")
		}
	}
}

// ===================== 主函数 =====================
func main() {
	if err := ensureConfigExists(); err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		os.Exit(1)
	}

	var err error
	globalCfg, err = loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	for {
		CleanDeadSessions()
		fmt.Println("\n=====================================================")
		fmt.Println("🚀 全能远程管理工具 [RDP+SSH+✔️trzsz传输+多监视器+无报错] ✨")
		fmt.Println("=====================================================")
		fmt.Println("1. Windows 远程管理 (RDP) - 支持多监视器选择")
		fmt.Println("2. Linux   远程管理 (SSH) - trzsz文件传输弹窗必生效")
		fmt.Println("q. 退出程序")
		choice := readInput("请选择管理类型 [1/2/q]: ")

		switch choice {
		case "1":
			showSubMenu(globalCfg, HostTypeRDP)
		case "2":
			showSubMenu(globalCfg, HostTypeSSH)
		case "q", "Q":
			fmt.Println("\n👋 感谢使用，再见！")
			return
		default:
			fmt.Println("❌ 无效选项，请输入 1/2 或 q 重试。")
		}
	}
}
