package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

const (
	nodeServiceUnitPath   = "/etc/systemd/system/speedquality-node.service"
	gateServiceUnitPath   = "/etc/systemd/system/speedquality-node-gate.service"
	updateServiceUnitPath = "/etc/systemd/system/speedquality-node-update.service"
	updateTimerUnitPath   = "/etc/systemd/system/speedquality-node-update.timer"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "[X] %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string, input io.Reader, output io.Writer) error {
	if len(arguments) == 0 {
		return menu(input, output)
	}
	path := configPath()
	switch arguments[0] {
	case "setup":
		return setupCommand(path, arguments[1:], input, output)
	case "init":
		return initCommand(path, arguments[1:], input, output)
	case "serve", "run":
		return serveCommand(path, output)
	case "status":
		return statusCommand(path, output)
	case "register":
		return registerCommand(path, output)
	case "unregister":
		return unregisterCommand(path, output)
	case "heartbeat":
		state, err := readRuntimeState(statePath(path), time.Now())
		if err != nil {
			return err
		}
		if err := sendHeartbeat(context.Background(), path, state); err != nil {
			return err
		}
		fmt.Fprintln(output, "心跳已提交。")
		return nil
	case "telemetry":
		return telemetryCommand(path, output)
	case "route-key":
		if len(arguments) != 2 || (arguments[1] != "rotate" && arguments[1] != "revoke") {
			return errors.New("用法: sq-node route-key rotate|revoke")
		}
		key, err := updatePlatformRouteKey(path, arguments[1])
		if err != nil {
			return err
		}
		if arguments[1] == "rotate" {
			fmt.Fprintf(output, "Route Key 已轮换，旧 Key 立即失效。\n新 Route Key: %s\n", key)
		} else {
			fmt.Fprintln(output, "Route Key 已撤销；节点注册和 API Token 保持有效。")
		}
		return nil
	case "preflight":
		config, err := loadConfig(path)
		if err != nil {
			return err
		}
		report := runSecurityPreflight(config)
		printSecurityReport(output, report)
		if report.severe() {
			return errors.New("存在严重安全项；公共调度必须保持关闭")
		}
		return nil
	case "emergency-stop":
		return emergencyStopCommand(path, arguments[1:], output)
	case "access":
		if len(arguments) != 2 {
			return errors.New("用法: sq-node access public|private|paused")
		}
		return updateAccess(path, arguments[1], output)
	case "limit":
		return updateLimit(path, arguments[1:], output)
	case "schedule":
		return updateSchedule(path, arguments[1:], output)
	case "update":
		return updateCommand(path, arguments[1:], output)
	case "diagnose":
		return diagnose(path, output)
	case "logs":
		return logsCommand(arguments[1:], output)
	case "service":
		return serviceCommand(path, arguments[1:], output)
	case "gate":
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		return runGate(ctx, path, output)
	case "gate-clean":
		cleanupLeaseFirewall()
		_ = saveGateStatus(gateStatusPath(path), gateStatus{UpdatedAt: time.Now().Unix()})
		return nil
	case "version", "--version", "-V":
		fmt.Fprintf(output, "sq-node %s\n", version)
		return nil
	case "help", "--help", "-h":
		printUsage(output)
		return nil
	default:
		return fmt.Errorf("未知命令: %s", arguments[0])
	}
}

func printUsage(output io.Writer) {
	fmt.Fprint(output, `sq-node - SpeedQuality 测速节点

用法:
  sq-node                         打开终端管理菜单
  sq-node setup [选项]            首次配置、注册并安装后台服务
  sq-node init [选项]             初始化节点配置
  sq-node serve                   前台运行测速服务
  sq-node register                验证所有权并注册到平台
  sq-node unregister              从平台注销，保留本地配置
  sq-node status                  显示节点状态和 Route Key
  sq-node access public           允许公共调度
  sq-node access private          仅允许 --node Route Key
  sq-node access paused           暂停所有新测速
  sq-node limit daily 20GB        设置每日公共流量
  sq-node limit total 40GB        设置包含自用任务的每日总流量
  sq-node limit speed 200         设置最高测速档位
  sq-node limit concurrency 2     设置总并发
  sq-node limit reserve 1         设置提供者保留并发
  sq-node schedule always         全天提供服务
  sq-node schedule 08:00-23:00   设置每日可用时间
  sq-node update --check          检查稳定通道更新
  sq-node update                  安装经过签名的更新
  sq-node heartbeat               立即同步配置和状态
  sq-node telemetry               查看当前心跳会上报的平台字段
  sq-node route-key rotate        轮换 Route Key，旧 Key 立即失效
  sq-node route-key revoke        撤销 Route Key，保留节点注册
  sq-node preflight               只读检查 SSH、监听端口和防火墙
  sq-node emergency-stop          立即撤销任务并隔离公网测速入口
  sq-node emergency-stop --recover 检查后恢复为 private 模式
  sq-node diagnose                运行本地诊断
  sq-node logs [条数]             查看最近的结构化服务日志，默认 200 条
  sq-node service install         安装并启动 systemd 服务
  sq-node service remove          删除 systemd 服务和更新定时器，保留配置和程序
`)
}

func initCommand(path string, arguments []string, input io.Reader, output io.Writer) error {
	return initCommandMode(path, arguments, input, output, isTerminalInput(input))
}

func initCommandMode(
	path string,
	arguments []string,
	input io.Reader,
	output io.Writer,
	interactive bool,
) error {
	return initCommandModeNamed(path, arguments, input, output, interactive, "init")
}

func initCommandModeNamed(
	path string,
	arguments []string,
	input io.Reader,
	output io.Writer,
	interactive bool,
	commandName string,
) error {
	config := defaultConfig()
	existing := Config{}
	registered := false
	if loaded, err := loadConfig(path); err == nil {
		existing = loaded
		config = loaded
		registered = loaded.NodeID != "" || loaded.RouteKey != "" || loaded.APIToken != ""
	}
	flags := flag.NewFlagSet(commandName, flag.ContinueOnError)
	flags.SetOutput(output)
	platform := flags.String("platform", config.PlatformURL, "平台 HTTPS 地址")
	region := flags.String("region", config.Region, "省份代码")
	carrier := flags.String("carrier", config.Carrier, "运营商 ct/cu/cm")
	ipv4 := flags.String("ipv4", config.PublicIPv4, "公网 IPv4")
	ipv6 := flags.String("ipv6", config.PublicIPv6, "公网 IPv6")
	port := flags.Int("port", config.Port, "监听端口，默认从 50000-59999 随机选择")
	speed := flags.Int("speed", config.MaxMbps, "最高档位 100/200/400")
	concurrency := flags.Int("concurrency", config.MaxConcurrency, "最大并发")
	reserve := flags.Int("reserve", config.ReserveConcurrency, "提供者保留并发")
	daily := flags.String("daily", formatByteLimit(config.DailyPublicBytes), "每日公共流量，例如 20GB")
	total := flags.String("total", formatByteLimit(config.DailyTotalBytes), "每日总流量，例如 40GB")
	access := flags.String("access", config.AccessMode, "public/private/paused")
	availability := flags.String("availability", config.Availability, "always 或 HH:MM-HH:MM")
	timezone := flags.String("timezone", config.Timezone, "Local、UTC 或 IANA 时区")
	updateMode := flags.String("update-mode", config.UpdateMode, "automatic/manual")
	label := flags.String("label", config.Label, "节点标签")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	reader, ok := input.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(input)
	}
	if interactive {
		*platform = prompt(reader, output, "平台地址", *platform)
		*region = prompt(reader, output, "省份代码", *region)
		*carrier = prompt(reader, output, "运营商 ct/cu/cm", *carrier)
		*ipv4 = prompt(reader, output, "公网 IPv4", *ipv4)
		*ipv6 = prompt(reader, output, "公网 IPv6（可留空）", *ipv6)
	}
	if *port == 0 {
		selected, err := randomPort()
		if err != nil {
			return err
		}
		*port = selected
	}
	dailyBytes, err := parseByteLimit(*daily)
	if err != nil {
		return err
	}
	totalBytes, err := parseByteLimit(*total)
	if err != nil {
		return err
	}
	config.Version = configVersion
	config.PlatformURL = strings.TrimSuffix(strings.TrimSpace(*platform), "/")
	config.Region = strings.ToLower(strings.TrimSpace(*region))
	config.Carrier = strings.ToLower(strings.TrimSpace(*carrier))
	config.PublicIPv4 = strings.TrimSpace(*ipv4)
	config.PublicIPv6 = strings.TrimSpace(*ipv6)
	config.Port = *port
	config.MaxMbps = *speed
	config.MaxConcurrency = *concurrency
	config.ReserveConcurrency = *reserve
	config.DailyPublicBytes = dailyBytes
	config.DailyTotalBytes = totalBytes
	config.AccessMode = strings.ToLower(*access)
	config.Availability = strings.ToLower(strings.TrimSpace(*availability))
	config.Timezone = strings.TrimSpace(*timezone)
	config.UpdateMode = strings.ToLower(strings.TrimSpace(*updateMode))
	config.Label = strings.TrimSpace(*label)
	if registered && registrationIdentityChanged(existing, config) {
		return errors.New("已注册节点不能直接修改平台、地址、端口、地区、运营商或标签；请先执行 sq-node unregister")
	}
	if err := saveConfig(path, config); err != nil {
		return err
	}
	fmt.Fprintf(output, "配置已保存: %s\n服务端口: %d\n", path, config.Port)
	if registered {
		return syncIfRegistered(path, output)
	}
	return nil
}

func setupCommand(path string, arguments []string, input io.Reader, output io.Writer) error {
	return setupCommandMode(path, arguments, input, output, isTerminalInput(input))
}

func setupCommandMode(
	path string,
	arguments []string,
	input io.Reader,
	output io.Writer,
	interactive bool,
) error {
	for _, argument := range arguments {
		if argument == "-h" || argument == "--help" {
			return initCommandModeNamed(path, arguments, input, output, false, "setup")
		}
	}
	if os.Geteuid() != 0 {
		return errors.New("首次设置和 systemd 服务安装需要 root 权限，请使用 sudo sq-node setup")
	}
	reader, ok := input.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(input)
	}
	prepared, err := prepareSetupArguments(path, arguments, reader, output, interactive)
	if err != nil {
		return err
	}
	if err := initCommandModeNamed(path, prepared, reader, output, false, "setup"); err != nil {
		return err
	}
	config, err := loadConfig(path)
	if err != nil {
		return err
	}
	report := runSecurityPreflight(config)
	printSecurityReport(output, report)
	if report.severe() && config.AccessMode == "public" {
		config.AccessMode = "private"
		if err := saveConfig(path, config); err != nil {
			return err
		}
		fmt.Fprintln(output, "[!] 检测到严重安全项，节点已强制保持 private；修复后可重新运行预检并手动开放。")
	}
	if interactive && config.NodeID == "" {
		for {
			printSetupSummary(output, config)
			fmt.Fprint(output, "[Enter] 安装并启用  [E] 高级设置  [Q] 退出: ")
			answer, readErr := reader.ReadString('\n')
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			switch strings.ToLower(strings.TrimSpace(answer)) {
			case "":
				goto confirmed
			case "q":
				fmt.Fprintln(output, "配置已保存，但节点尚未注册或启动。")
				return nil
			case "e":
				if err := initCommandModeNamed(path, nil, reader, output, true, "init"); err != nil {
					return err
				}
				if err := configureLimitsInteractive(path, reader, output); err != nil {
					return err
				}
				config, err = loadConfig(path)
				if err != nil {
					return err
				}
				config.AccessMode = strings.ToLower(prompt(
					reader, output, "访问模式 public/private/paused", config.AccessMode,
				))
				config.Availability = strings.ToLower(prompt(
					reader, output, "可用时间 always 或 HH:MM-HH:MM", config.Availability,
				))
				config.Timezone = prompt(reader, output, "时区", config.Timezone)
				config.UpdateMode = strings.ToLower(prompt(
					reader, output, "更新模式 automatic/manual", config.UpdateMode,
				))
				if err := saveConfig(path, config); err != nil {
					return err
				}
			default:
				fmt.Fprintln(output, "请输入 Enter、E 或 Q。")
			}
		}
	}

confirmed:
	config, err = loadConfig(path)
	if err != nil {
		return err
	}
	report = runSecurityPreflight(config)
	if report.severe() && config.AccessMode == "public" {
		config.AccessMode = "private"
		if err := saveConfig(path, config); err != nil {
			return err
		}
		fmt.Fprintln(output, "[!] 高级设置后安全预检仍有严重项，节点保持 private。")
	}
	if config.NodeID == "" {
		fmt.Fprintf(output,
			"正在通过 TCP %d 完成公网可达性和所有权验证。\n",
			config.Port,
		)
		registered, registerErr := registerWithPlatform(path)
		if registerErr != nil {
			return registerErr
		}
		config = registered
		fmt.Fprintf(output, "节点注册成功，Route Key: %s\n", config.RouteKey)
		if config.AdmissionStatus == "observing" {
			fmt.Fprintln(output, "节点正在观察期；Route Key 可立即使用，通过连续健康检查后自动加入公共调度。")
		}
	} else {
		fmt.Fprintf(output, "节点已经注册，继续检查后台服务。Route Key: %s\n", config.RouteKey)
	}
	if err := serviceCommand(path, []string{"install"}, output); err != nil {
		return err
	}
	fmt.Fprintln(output, "设置完成。以后运行 sudo sq-node 即可查看状态或修改额度。")
	return nil
}

func setupFlag(arguments []string, name string) (string, bool) {
	aliases := []string{name}
	if strings.HasPrefix(name, "--") {
		aliases = append(aliases, "-"+strings.TrimPrefix(name, "--"))
	}
	for index, argument := range arguments {
		for _, alias := range aliases {
			if argument == alias && index+1 < len(arguments) {
				return arguments[index+1], true
			}
			if strings.HasPrefix(argument, alias+"=") {
				return strings.TrimPrefix(argument, alias+"="), true
			}
		}
	}
	return "", false
}

func appendSetupFlag(arguments []string, name, value string) []string {
	if value == "" {
		return arguments
	}
	if _, exists := setupFlag(arguments, name); exists {
		return arguments
	}
	return append(arguments, name, value)
}

func prepareSetupArguments(
	path string,
	arguments []string,
	reader *bufio.Reader,
	output io.Writer,
	interactive bool,
) ([]string, error) {
	prepared := append([]string(nil), arguments...)
	config := defaultConfig()
	if existing, err := loadConfig(path); err == nil {
		config = existing
	}
	platform, provided := setupFlag(prepared, "--platform")
	if !provided {
		platform = config.PlatformURL
	}
	platform = strings.TrimSuffix(strings.TrimSpace(platform), "/")
	if platform == "" {
		return nil, errors.New("缺少平台地址；请使用 --platform https://<SpeedQuality 域名>")
	}
	parsedPlatform, parseErr := url.Parse(platform)
	if parseErr != nil || parsedPlatform.Scheme != "https" || parsedPlatform.Hostname() == "" ||
		parsedPlatform.User != nil || parsedPlatform.Path != "" || parsedPlatform.RawQuery != "" ||
		parsedPlatform.Fragment != "" {
		return nil, errors.New("平台地址必须是没有尾部路径的 HTTPS URL")
	}
	prepared = appendSetupFlag(prepared, "--platform", platform)

	region, regionSet := setupFlag(prepared, "--region")
	carrier, carrierSet := setupFlag(prepared, "--carrier")
	ipv4, ipv4Set := setupFlag(prepared, "--ipv4")
	ipv6, ipv6Set := setupFlag(prepared, "--ipv6")
	if !regionSet {
		region = config.Region
	}
	if !carrierSet {
		carrier = config.Carrier
	}
	if !ipv4Set {
		ipv4 = config.PublicIPv4
	}
	if !ipv6Set {
		ipv6 = config.PublicIPv6
	}

	if region == "" || carrier == "" || (ipv4 == "" && ipv6 == "") {
		fmt.Fprintln(output, "正在自动检测公网地址、地区和运营商……")
		detected, err := detectNodeSetup(platform)
		if err != nil {
			return nil, err
		}
		if ipv4 == "" {
			ipv4 = detected.IPv4
		}
		if ipv6 == "" {
			ipv6 = detected.IPv6
		}
		if region == "" {
			region = detected.Region
		}
		if carrier == "" {
			carrier = detected.Carrier
		}
		fmt.Fprintf(output, "检测结果: IPv4 %s / IPv6 %s / 地区 %s (%s) / 运营商 %s (%s)\n",
			emptyDash(ipv4), emptyDash(ipv6), emptyDash(regionNames[region]), emptyDash(region),
			emptyDash(carrierNames[carrier]), emptyDash(carrier))
	}
	if region == "" && interactive {
		region = prompt(reader, output, "无法自动识别省份，请输入省份代码", "")
	}
	if carrier == "" && interactive {
		carrier = prompt(reader, output, "无法自动识别运营商，请输入 ct/cu/cm", "")
	}
	if region == "" || carrier == "" {
		return nil, errors.New("无法自动识别地区或运营商；请使用 --region 和 --carrier 明确指定")
	}
	prepared = appendSetupFlag(prepared, "--region", region)
	prepared = appendSetupFlag(prepared, "--carrier", carrier)
	prepared = appendSetupFlag(prepared, "--ipv4", ipv4)
	prepared = appendSetupFlag(prepared, "--ipv6", ipv6)
	return prepared, nil
}

func printSetupSummary(output io.Writer, config Config) {
	fmt.Fprintf(output, `
SpeedQuality 节点配置

公网 IPv4：    %s
IPv6：         %s
地区：         %s (%s)
运营商：       %s (%s)
所有权验证端口：%d/TCP（仅注册时临时监听）
临时租约端口：  50000-59999/TCP（云安全组允许，主机按来源临时放行）
公开调度：     %s
最高档位：     %d Mbps
最大并发：     %d
每日公共额度： %s
每日总额度：   %s
自动更新：     %s
运行时间：     %s (%s)
服务账户：     speedquality
运行上报：     健康、容量、可用状态和总流量
攻击来源上报： 关闭

`, emptyDash(config.PublicIPv4), emptyDash(config.PublicIPv6), regionNames[config.Region], config.Region,
		carrierNames[config.Carrier], config.Carrier, config.Port, config.AccessMode, config.MaxMbps,
		config.MaxConcurrency, formatByteLimit(config.DailyPublicBytes), formatByteLimit(config.DailyTotalBytes),
		config.UpdateChannel, config.Availability, config.Timezone)
}

func registrationIdentityChanged(left, right Config) bool {
	return left.PlatformURL != right.PlatformURL || left.PublicIPv4 != right.PublicIPv4 ||
		left.PublicIPv6 != right.PublicIPv6 || left.Port != right.Port ||
		left.Region != right.Region || left.Carrier != right.Carrier || left.Label != right.Label
}

func serveCommand(path string, output io.Writer) error {
	node, err := newNodeServer(path)
	if err != nil {
		return err
	}
	config, _ := loadConfig(path)
	listener, err := listenForConfig(config)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "sq-node 正在监听 %s\n", listener.Addr())
	errorsChannel := make(chan error, 1)
	go func() { errorsChannel <- node.serve(listener) }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case received := <-signals:
		fmt.Fprintf(output, "收到 %s，正在停止。\n", received)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		return node.shutdown(ctx)
	case serveErr := <-errorsChannel:
		if errors.Is(serveErr, context.Canceled) || errors.Is(serveErr, os.ErrClosed) ||
			errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return serveErr
	}
}

func emergencyStopCommand(path string, arguments []string, output io.Writer) error {
	if len(arguments) > 1 || (len(arguments) == 1 && arguments[0] != "--recover") {
		return errors.New("用法: sq-node emergency-stop [--recover]")
	}
	if os.Geteuid() != 0 {
		return errors.New("紧急隔离和恢复需要 root 权限，请使用 sudo")
	}
	config, err := loadConfig(path)
	if err != nil {
		return err
	}
	recovering := len(arguments) == 1
	if recovering {
		report := runSecurityPreflight(config)
		printSecurityReport(output, report)
		if report.severe() {
			return errors.New("安全预检仍有严重问题，拒绝恢复公网入口")
		}
		if managedNodeServicesInstalled() {
			if err := stopManagedNodeServices(false); err != nil {
				return err
			}
		}
		state, stateErr := loadRuntimeState(statePath(path), time.Now())
		if stateErr != nil {
			return stateErr
		}
		config.AccessMode = "private"
		if err := saveConfig(path, config); err != nil {
			return err
		}
		if err := state.setEmergencyStopped(false); err != nil {
			return err
		}
		_ = syncIfRegistered(path, output)
		if managedNodeServicesInstalled() {
			if err := startManagedNodeServices(true); err != nil {
				return err
			}
		}
		fmt.Fprintln(output, "紧急隔离已解除，节点恢复为 private；确认正常后再执行 sq-node access public。")
		return nil
	}
	config.AccessMode = "paused"
	if err := saveConfig(path, config); err != nil {
		return err
	}
	if managedNodeServicesInstalled() {
		if err := stopManagedNodeServices(false); err != nil {
			return err
		}
	} else {
		cleanupLeaseFirewall()
		_ = os.Remove(gateStatusPath(path))
	}
	state, err := loadRuntimeState(statePath(path), time.Now())
	if err != nil {
		return err
	}
	if err := state.setEmergencyStopped(true); err != nil {
		return err
	}
	_ = syncIfRegistered(path, output)
	if managedNodeServicesInstalled() {
		if err := startManagedNodeServices(false); err != nil {
			return err
		}
	}
	fmt.Fprintln(output, "紧急隔离已生效：活动任务已撤销，公网测速入口已停止，重启后仍保持隔离。")
	return nil
}

func managedNodeServicesInstalled() bool {
	_, err := os.Stat(nodeServiceUnitPath)
	return err == nil
}

func runSystemctl(arguments ...string) error {
	output, err := updateSystemctl(arguments...)
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("systemctl %s 失败: %s", strings.Join(arguments, " "), message)
	}
	return nil
}

func stopManagedNodeServices(disable bool) error {
	action := []string{"stop"}
	if disable {
		action = []string{"disable", "--now"}
	}
	if err := runSystemctl(append(action, "speedquality-node-gate.service")...); err != nil {
		return err
	}
	cleanupLeaseFirewall()
	if err := runSystemctl(append(action, "speedquality-node.service")...); err != nil {
		return err
	}
	return nil
}

func startManagedNodeServices(includeGate bool) error {
	if err := runSystemctl("enable", "--now", "speedquality-node.service"); err != nil {
		return err
	}
	if includeGate {
		if err := runSystemctl("enable", "--now", "speedquality-node-gate.service"); err != nil {
			return err
		}
	}
	return nil
}

func registerCommand(path string, output io.Writer) error {
	managed := os.Geteuid() == 0 && managedNodeServicesInstalled()
	if managed {
		if err := stopManagedNodeServices(false); err != nil {
			return err
		}
		_ = os.Remove(gateStatusPath(path))
	}
	config, err := registerWithPlatform(path)
	if err != nil {
		if managed {
			_ = startManagedNodeServices(false)
		}
		return err
	}
	if managed {
		if err := startManagedNodeServices(true); err != nil {
			return err
		}
	}
	fmt.Fprintf(output, "节点注册成功\nNode ID: %s\nRoute Key: %s\n", config.NodeID, config.RouteKey)
	return nil
}

func unregisterCommand(path string, output io.Writer) error {
	managed := os.Geteuid() == 0 && managedNodeServicesInstalled()
	if managed {
		if err := stopManagedNodeServices(true); err != nil {
			return err
		}
	}
	if err := unregisterFromPlatform(path); err != nil {
		if managed {
			_ = startManagedNodeServices(true)
		}
		return err
	}
	cleanupLeaseFirewall()
	_ = os.Remove(gateStatusPath(path))
	fmt.Fprintln(output, "节点已从平台注销；本地配置、状态、程序和 systemd 单元均已保留，节点服务已停止。")
	return nil
}

func statusCommand(path string, output io.Writer) error {
	config, err := loadConfig(path)
	if err != nil {
		return err
	}
	state, err := readRuntimeState(statePath(path), time.Now())
	if err != nil {
		return err
	}
	active, publicActive, publicBytes, publicReserved, totalBytes, totalReserved, emergencyStopped := state.snapshot(time.Now())
	registered := "否"
	if config.NodeID != "" {
		registered = "是"
	}
	gate := currentGateStatus(path, time.Now())
	gateState := "不可用"
	if gate.Ready {
		gateState = "就绪"
	}
	if gate.LastError != "" {
		gateState += "（" + safeLogText(gate.LastError) + "）"
	}
	fmt.Fprintf(output,
		"配置: %s\n注册: %s\nNode ID: %s\nRoute Key: %s\n地区/运营商: %s / %s\n公网地址: %s %s\n内部端口: %d\n临时租约网关: %s（活动租约 %d）\n访问模式: %s\n紧急隔离: %t\n平台准入: %s（认证 %d Mbps）\n当前可用: %t\n可用时间: %s (%s)\n最高档位: %d Mbps\n并发: %d（保留 %d）\n每日公共额度: %s\n每日总额度: %s\n今日公共流量: %s（另预留 %s）\n今日总流量: %s（另预留 %s）\n自动更新: %s/%s\n当前任务: %d（公共 %d）\n",
		path, registered, emptyDash(config.NodeID), emptyDash(config.RouteKey), config.Region,
		config.Carrier, emptyDash(config.PublicIPv4), emptyDash(config.PublicIPv6), config.Port,
		gateState, gate.ActiveLeases, config.AccessMode, emergencyStopped,
		emptyDash(config.AdmissionStatus), config.CertifiedMbps,
		availableAt(config, time.Now()), config.Availability, config.Timezone,
		config.MaxMbps, config.MaxConcurrency, config.ReserveConcurrency,
		formatByteLimit(config.DailyPublicBytes), formatByteLimit(config.DailyTotalBytes),
		formatByteLimit(publicBytes), formatByteLimit(publicReserved),
		formatByteLimit(totalBytes), formatByteLimit(totalReserved),
		config.UpdateMode, config.UpdateChannel, active, publicActive,
	)
	return nil
}

func telemetryCommand(path string, output io.Writer) error {
	config, err := loadConfig(path)
	if err != nil {
		return err
	}
	state, err := readRuntimeState(statePath(path), time.Now())
	if err != nil {
		return err
	}
	payload := buildHeartbeatPayload(path, config, state, time.Now())
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(output, "当前认证心跳会上报以下运行字段；不会上报攻击来源、其它端口连接或用户文件：")
	fmt.Fprintln(output, string(data))
	return nil
}

func updateAccess(path, mode string, output io.Writer) error {
	config, err := loadConfig(path)
	if err != nil {
		return err
	}
	mode = strings.ToLower(mode)
	if mode != "public" && mode != "private" && mode != "paused" {
		return errors.New("访问模式必须是 public、private 或 paused")
	}
	state, stateErr := readRuntimeState(statePath(path), time.Now())
	if stateErr != nil {
		return stateErr
	}
	_, _, _, _, _, _, emergencyStopped := state.snapshot(time.Now())
	if emergencyStopped && mode != "paused" {
		return errors.New("节点处于紧急隔离；请先执行 sq-node emergency-stop --recover")
	}
	if mode == "public" {
		report := runSecurityPreflight(config)
		printSecurityReport(output, report)
		if report.severe() {
			return errors.New("安全预检存在严重问题，拒绝开启公共调度")
		}
	}
	config.AccessMode = mode
	if err := saveConfig(path, config); err != nil {
		return err
	}
	fmt.Fprintf(output, "访问模式已切换为 %s。\n", mode)
	return syncIfRegistered(path, output)
}

func updateLimit(path string, arguments []string, output io.Writer) error {
	if len(arguments) != 2 {
		return errors.New("用法: sq-node limit daily|total|speed|concurrency|reserve VALUE")
	}
	config, err := loadConfig(path)
	if err != nil {
		return err
	}
	switch arguments[0] {
	case "daily", "public":
		config.DailyPublicBytes, err = parseByteLimit(arguments[1])
	case "total":
		config.DailyTotalBytes, err = parseByteLimit(arguments[1])
	case "speed":
		config.MaxMbps, err = strconv.Atoi(arguments[1])
	case "concurrency":
		config.MaxConcurrency, err = strconv.Atoi(arguments[1])
	case "reserve":
		config.ReserveConcurrency, err = strconv.Atoi(arguments[1])
	default:
		return errors.New("未知额度类型")
	}
	if err != nil {
		return err
	}
	if err := saveConfig(path, config); err != nil {
		return err
	}
	fmt.Fprintln(output, "节点限制已更新。")
	return syncIfRegistered(path, output)
}

func updateSchedule(path string, arguments []string, output io.Writer) error {
	if len(arguments) < 1 || len(arguments) > 2 {
		return errors.New("用法: sq-node schedule always|HH:MM-HH:MM[,HH:MM-HH:MM] [时区]")
	}
	config, err := loadConfig(path)
	if err != nil {
		return err
	}
	config.Availability = strings.ToLower(strings.TrimSpace(arguments[0]))
	if len(arguments) == 2 {
		config.Timezone = strings.TrimSpace(arguments[1])
	}
	if err := saveConfig(path, config); err != nil {
		return err
	}
	fmt.Fprintf(output, "可用时间已设置为 %s (%s)。\n", config.Availability, config.Timezone)
	return syncIfRegistered(path, output)
}

func syncIfRegistered(path string, output io.Writer) error {
	config, err := loadConfig(path)
	if err != nil || config.APIToken == "" {
		return err
	}
	state, err := readRuntimeState(statePath(path), time.Now())
	if err != nil {
		return err
	}
	if err := sendHeartbeat(context.Background(), path, state); err != nil {
		fmt.Fprintf(output, "[!] 本地配置已保存，平台同步将在后续心跳重试: %v\n", err)
	}
	return nil
}

func diagnose(path string, output io.Writer) error {
	config, err := loadConfig(path)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, "[+] 配置文件有效")
	if _, err := publicKeyFromJWK(config.JWTPublicKey); config.NodeID != "" && err != nil {
		return err
	} else if config.NodeID != "" {
		fmt.Fprintln(output, "[+] JWT 公钥有效")
	}
	if localNodeResponds(config) {
		fmt.Fprintln(output, "[+] 本地节点服务可访问")
	} else {
		listener, listenErr := listenForConfig(config)
		if listenErr != nil {
			return fmt.Errorf("服务未运行且端口不可用: %w", listenErr)
		}
		_ = listener.Close()
		fmt.Fprintln(output, "[!] 服务未运行，端口当前可用")
	}
	if config.APIToken != "" {
		state, stateErr := readRuntimeState(statePath(path), time.Now())
		if stateErr != nil {
			return stateErr
		}
		if heartbeatErr := sendHeartbeat(context.Background(), path, state); heartbeatErr != nil {
			return heartbeatErr
		}
		fmt.Fprintln(output, "[+] 平台鉴权和心跳正常")
	}
	report := runSecurityPreflight(config)
	printSecurityReport(output, report)
	return nil
}

func logsCommand(arguments []string, output io.Writer) error {
	lines := 200
	if len(arguments) > 1 {
		return errors.New("用法: sq-node logs [20-2000]")
	}
	if len(arguments) == 1 {
		parsed, err := strconv.Atoi(arguments[0])
		if err != nil || parsed < 20 || parsed > 2000 {
			return errors.New("日志条数必须在 20 到 2000 之间")
		}
		lines = parsed
	}
	command := exec.Command(
		"journalctl", "-u", "speedquality-node.service", "-n", strconv.Itoa(lines),
		"--no-pager", "-o", "cat",
	)
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return fmt.Errorf("无法读取 systemd 日志: %w", err)
	}
	return nil
}

func serviceCommand(path string, arguments []string, output io.Writer) error {
	if len(arguments) != 1 || (arguments[0] != "install" && arguments[0] != "remove") {
		return errors.New("用法: sq-node service install|remove")
	}
	if os.Geteuid() != 0 {
		return errors.New("systemd 服务管理需要 root 权限")
	}
	if arguments[0] == "remove" {
		_, _ = updateSystemctl("disable", "--now", "speedquality-node-gate.service")
		_, _ = updateSystemctl("disable", "--now", "speedquality-node.service")
		_, _ = updateSystemctl("disable", "--now", "speedquality-node-update.timer")
		cleanupLeaseFirewall()
		_ = os.Remove(gateStatusPath(path))
		if err := os.Remove(nodeServiceUnitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, updatePath := range []string{gateServiceUnitPath, updateServiceUnitPath, updateTimerUnitPath} {
			if err := os.Remove(updatePath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		_ = runSystemctl("daemon-reload")
		fmt.Fprintln(output, "systemd 服务已删除；二进制、配置和状态数据均已保留。")
		return nil
	}
	if _, err := loadConfig(path); err != nil {
		return err
	}
	if err := ensureServiceAccount(path); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, _ = filepath.Abs(executable)
	unit := fmt.Sprintf(`[Unit]
Description=SpeedQuality Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=speedquality
Group=speedquality
ExecStart=%s serve
Environment=SQ_NODE_CONFIG=%s
Environment=SQ_NODE_INTERNAL_ONLY=1
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=%s
CapabilityBoundingSet=
RestrictAddressFamilies=AF_INET AF_INET6
RestrictSUIDSGID=true
LockPersonality=true
MemoryMax=256M
TasksMax=128
LimitNOFILE=4096

[Install]
WantedBy=multi-user.target
`, executable, path, filepath.Dir(path))
	if err := os.WriteFile(nodeServiceUnitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	gateUnit := fmt.Sprintf(`[Unit]
Description=SpeedQuality temporary source lease gateway
After=network-online.target speedquality-node.service
Wants=network-online.target
Requires=speedquality-node.service

[Service]
Type=simple
User=speedquality
Group=speedquality
ExecStart=%s gate
ExecStopPost=-%s gate-clean
Environment=SQ_NODE_CONFIG=%s
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=%s
AmbientCapabilities=CAP_NET_ADMIN
CapabilityBoundingSet=CAP_NET_ADMIN
RestrictAddressFamilies=AF_INET AF_INET6 AF_NETLINK
RestrictSUIDSGID=true
LockPersonality=true
MemoryMax=128M
TasksMax=96
LimitNOFILE=4096

[Install]
WantedBy=multi-user.target
`, executable, executable, path, filepath.Dir(path))
	if err := os.WriteFile(gateServiceUnitPath, []byte(gateUnit), 0o644); err != nil {
		return err
	}
	updateUnit := fmt.Sprintf(`[Unit]
Description=SpeedQuality Node signed update
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=%s update --automatic
Environment=SQ_NODE_CONFIG=%s
`, executable, path)
	updateTimer := `[Unit]
Description=Check SpeedQuality Node stable updates

[Timer]
OnBootSec=20min
OnUnitActiveSec=6h
RandomizedDelaySec=30min
Persistent=true

[Install]
WantedBy=timers.target
`
	if err := os.WriteFile(updateServiceUnitPath, []byte(updateUnit), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(updateTimerUnitPath, []byte(updateTimer), 0o644); err != nil {
		return err
	}
	if err := runSystemctl("daemon-reload"); err != nil {
		return err
	}
	if err := runSystemctl("enable", "speedquality-node.service"); err != nil {
		return err
	}
	if err := runSystemctl("restart", "speedquality-node.service"); err != nil {
		return err
	}
	if err := runSystemctl("enable", "--now", "speedquality-node-gate.service"); err != nil {
		return err
	}
	if err := runSystemctl("enable", "--now", "speedquality-node-update.timer"); err != nil {
		return err
	}
	fmt.Fprintln(output, "systemd 服务与签名更新定时器已安装并启动。")
	return nil
}

func configureLimitsInteractive(path string, reader *bufio.Reader, output io.Writer) error {
	config, err := loadConfig(path)
	if err != nil {
		return err
	}
	speed, err := strconv.Atoi(prompt(
		reader, output, "最高档位 100/200/400", strconv.Itoa(config.MaxMbps),
	))
	if err != nil {
		return errors.New("最高档位必须是数字")
	}
	concurrency, err := strconv.Atoi(prompt(
		reader, output, "最大并发", strconv.Itoa(config.MaxConcurrency),
	))
	if err != nil {
		return errors.New("最大并发必须是数字")
	}
	reserve, err := strconv.Atoi(prompt(
		reader, output, "提供者保留并发", strconv.Itoa(config.ReserveConcurrency),
	))
	if err != nil {
		return errors.New("保留并发必须是数字")
	}
	daily, err := parseByteLimit(prompt(
		reader, output, "每日公共流量，例如 20GB", formatByteLimit(config.DailyPublicBytes),
	))
	if err != nil {
		return err
	}
	total, err := parseByteLimit(prompt(
		reader, output, "每日总流量，例如 40GB", formatByteLimit(config.DailyTotalBytes),
	))
	if err != nil {
		return err
	}
	config.MaxMbps = speed
	config.MaxConcurrency = concurrency
	config.ReserveConcurrency = reserve
	config.DailyPublicBytes = daily
	config.DailyTotalBytes = total
	if err := saveConfig(path, config); err != nil {
		return err
	}
	fmt.Fprintln(output, "节点流量和容量限制已更新。")
	return syncIfRegistered(path, output)
}

func menu(input io.Reader, output io.Writer) error {
	reader := bufio.NewReader(input)
	for {
		fmt.Fprint(output, `
SpeedQuality 节点管理
  1) 首次设置（配置、注册、后台启动）
  2) 查看状态
  3) 修改配置
  4) 注册节点
  5) 设置访问模式
  6) 设置流量和容量
  7) 运行诊断
  8) 安装 systemd 服务
  9) 注销节点
 10) 设置可用时间
 11) 检查并安装更新
 12) 查看最近日志
 13) 运行安全预检
 14) 轮换或撤销 Route Key
 15) 紧急隔离或恢复
 16) 查看平台上报字段
  0) 退出
请选择: `)
		answer, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		switch strings.TrimSpace(answer) {
		case "1":
			if err := setupCommandMode(configPath(), nil, reader, output, true); err != nil {
				fmt.Fprintf(output, "[X] %v\n", err)
			}
		case "2":
			if err := statusCommand(configPath(), output); err != nil {
				fmt.Fprintf(output, "[X] %v\n", err)
			}
		case "3":
			if err := initCommandMode(configPath(), nil, reader, output, true); err != nil {
				fmt.Fprintf(output, "[X] %v\n", err)
			}
		case "4":
			if registerErr := registerCommand(configPath(), output); registerErr != nil {
				fmt.Fprintf(output, "[X] %v\n", registerErr)
			}
		case "5":
			defaultMode := "public"
			if current, loadErr := loadConfig(configPath()); loadErr == nil {
				defaultMode = current.AccessMode
			}
			mode := prompt(reader, output, "访问模式 public/private/paused", defaultMode)
			if err := updateAccess(configPath(), mode, output); err != nil {
				fmt.Fprintf(output, "[X] %v\n", err)
			}
		case "6":
			if err := configureLimitsInteractive(configPath(), reader, output); err != nil {
				fmt.Fprintf(output, "[X] %v\n", err)
			}
		case "7":
			if err := diagnose(configPath(), output); err != nil {
				fmt.Fprintf(output, "[X] %v\n", err)
			}
		case "8":
			if err := serviceCommand(configPath(), []string{"install"}, output); err != nil {
				fmt.Fprintf(output, "[X] %v\n", err)
			}
		case "9":
			if err := unregisterCommand(configPath(), output); err != nil {
				fmt.Fprintf(output, "[X] %v\n", err)
			}
		case "10":
			current, loadErr := loadConfig(configPath())
			if loadErr != nil {
				fmt.Fprintf(output, "[X] %v\n", loadErr)
				break
			}
			schedule := prompt(reader, output, "可用时间 always 或 HH:MM-HH:MM", current.Availability)
			timezone := prompt(reader, output, "时区", current.Timezone)
			if err := updateSchedule(configPath(), []string{schedule, timezone}, output); err != nil {
				fmt.Fprintf(output, "[X] %v\n", err)
			}
		case "11":
			if err := updateCommand(configPath(), nil, output); err != nil {
				fmt.Fprintf(output, "[X] %v\n", err)
			}
		case "12":
			if err := logsCommand(nil, output); err != nil {
				fmt.Fprintf(output, "[X] %v\n", err)
			}
		case "13":
			current, loadErr := loadConfig(configPath())
			if loadErr != nil {
				fmt.Fprintf(output, "[X] %v\n", loadErr)
				break
			}
			report := runSecurityPreflight(current)
			printSecurityReport(output, report)
		case "14":
			action := strings.ToLower(prompt(reader, output, "操作 rotate/revoke", "rotate"))
			key, updateErr := updatePlatformRouteKey(configPath(), action)
			if updateErr != nil {
				fmt.Fprintf(output, "[X] %v\n", updateErr)
			} else if action == "rotate" {
				fmt.Fprintf(output, "Route Key 已轮换: %s\n", key)
			} else {
				fmt.Fprintln(output, "Route Key 已撤销。")
			}
		case "15":
			action := strings.ToLower(prompt(reader, output, "操作 stop/recover", "stop"))
			arguments := []string{}
			if action == "recover" {
				arguments = []string{"--recover"}
			} else if action != "stop" {
				fmt.Fprintln(output, "[X] 请输入 stop 或 recover。")
				break
			}
			if err := emergencyStopCommand(configPath(), arguments, output); err != nil {
				fmt.Fprintf(output, "[X] %v\n", err)
			}
		case "16":
			if err := telemetryCommand(configPath(), output); err != nil {
				fmt.Fprintf(output, "[X] %v\n", err)
			}
		case "0", "":
			return nil
		default:
			fmt.Fprintln(output, "无效选项。")
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func prompt(reader *bufio.Reader, output io.Writer, label, defaultValue string) string {
	fmt.Fprintf(output, "%s [%s]: ", label, defaultValue)
	value, _ := reader.ReadString('\n')
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultValue
	}
	return value
}

func isTerminalInput(input io.Reader) bool {
	file, ok := input.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func parseByteLimit(value string) (int64, error) {
	text := strings.ToUpper(strings.TrimSpace(value))
	multiplier := int64(1)
	for suffix, factor := range map[string]int64{
		"TB": 1_000_000_000_000, "GB": 1_000_000_000, "MB": 1_000_000,
	} {
		if strings.HasSuffix(text, suffix) {
			multiplier = factor
			text = strings.TrimSpace(strings.TrimSuffix(text, suffix))
			break
		}
	}
	amount, err := strconv.ParseFloat(text, 64)
	if err != nil || amount < 0 {
		return 0, errors.New("流量额度格式无效")
	}
	bytes := amount * float64(multiplier)
	if bytes > 10_000_000_000_000 {
		return 0, errors.New("流量额度过大")
	}
	return int64(bytes), nil
}

func formatByteLimit(value int64) string {
	if value >= 1_000_000_000_000 && value%1_000_000_000_000 == 0 {
		return fmt.Sprintf("%dTB", value/1_000_000_000_000)
	}
	if value >= 1_000_000_000 && value%1_000_000_000 == 0 {
		return fmt.Sprintf("%dGB", value/1_000_000_000)
	}
	if value >= 1_000_000 && value%1_000_000 == 0 {
		return fmt.Sprintf("%dMB", value/1_000_000)
	}
	return strconv.FormatInt(value, 10)
}

func emptyDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
