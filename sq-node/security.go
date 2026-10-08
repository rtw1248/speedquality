package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type securityFinding struct {
	Severity string
	Code     string
	Message  string
}

type securityReport struct {
	Findings []securityFinding
}

func (report securityReport) severe() bool {
	for _, finding := range report.Findings {
		if finding.Severity == "severe" {
			return true
		}
	}
	return false
}

var securityLookPath = exec.LookPath

var securityCommand = func(name string, arguments ...string) ([]byte, error) {
	return exec.Command(name, arguments...).CombinedOutput()
}

func runSecurityPreflight(config Config) securityReport {
	report := securityReport{}
	add := func(severity, code, message string) {
		report.Findings = append(report.Findings, securityFinding{
			Severity: severity,
			Code:     code,
			Message:  message,
		})
	}
	if _, err := securityLookPath("nft"); err != nil {
		add("severe", "nftables_missing", "未找到 nft；公共节点无法启用临时来源地址放行")
	} else if _, err := securityCommand("nft", "list", "ruleset"); err != nil {
		add("severe", "nftables_unavailable", "无法只读检查 nftables；请使用 root 重新检查")
	} else {
		add("ok", "nftables_ready", "nftables 可用")
	}

	sshdOutput, sshdErr := securityCommand("sshd", "-T")
	if sshdErr == nil {
		settings := parseSSHDSettings(string(sshdOutput))
		rootLogin := settings["permitrootlogin"]
		passwordLogin := settings["passwordauthentication"]
		interactiveLogin := settings["kbdinteractiveauthentication"]
		if rootLogin == "yes" && passwordLogin == "yes" {
			add("severe", "ssh_root_password", "SSH 同时允许 root 与密码登录")
		} else if passwordLogin == "yes" || interactiveLogin == "yes" {
			add("severe", "ssh_public_password", "SSH 允许密码或键盘交互认证；公共测速节点应使用密钥登录")
		} else if rootLogin == "yes" {
			add("warning", "ssh_root_key", "SSH 允许 root 使用密钥登录，请确认密钥和登录来源限制")
		} else {
			add("ok", "ssh_effective_config", "SSH 有效配置未发现 root 密码登录组合")
		}
	} else {
		add("warning", "sshd_config_unknown", "无法读取 sshd 有效配置，请提供者自行确认 SSH 安全")
	}

	if output, err := securityCommand("ss", "-H", "-lnt"); err == nil {
		for _, port := range exposedHighRiskPorts(string(output)) {
			add("severe", "public_high_risk_port", fmt.Sprintf("发现高风险服务端口 %d 监听公网地址", port))
		}
	} else {
		add("warning", "listeners_unknown", "无法读取 TCP 监听列表")
	}

	if config.Port < 50000 || config.Port > 59999 {
		add("severe", "node_port_invalid", "测速节点端口不在 50000-59999 范围")
	}
	return report
}

func parseSSHDSettings(value string) map[string]string {
	settings := map[string]string{}
	for _, line := range strings.Split(value, "\n") {
		fields := strings.Fields(strings.ToLower(strings.TrimSpace(line)))
		if len(fields) >= 2 {
			settings[fields[0]] = fields[1]
		}
	}
	return settings
}

func exposedHighRiskPorts(value string) []int {
	risky := map[int]bool{21: true, 23: true, 2375: true, 3306: true, 5432: true, 6379: true, 11211: true, 27017: true}
	seen := map[int]bool{}
	var ports []int
	for _, line := range strings.Split(value, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		address := fields[3]
		separator := strings.LastIndex(address, ":")
		if separator < 0 {
			continue
		}
		host := strings.Trim(address[:separator], "[]")
		port, err := strconv.Atoi(address[separator+1:])
		if err != nil || !risky[port] || (host != "0.0.0.0" && host != "::" && host != "*") || seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	return ports
}

func printSecurityReport(output io.Writer, report securityReport) {
	fmt.Fprintln(output, "安全预检（只读，不会修改 SSH 或现有防火墙）：")
	for _, finding := range report.Findings {
		mark := "+"
		if finding.Severity == "warning" {
			mark = "!"
		} else if finding.Severity == "severe" {
			mark = "X"
		}
		fmt.Fprintf(output, "[%s] %s\n", mark, finding.Message)
	}
}

func ensureServiceAccount(path string) error {
	if _, err := securityCommand("id", "-u", "speedquality"); err != nil {
		shell := "/usr/sbin/nologin"
		if _, statErr := os.Stat(shell); statErr != nil {
			shell = "/sbin/nologin"
		}
		if output, createErr := securityCommand(
			"useradd", "--system", "--user-group", "--home-dir", filepath.Dir(path),
			"--no-create-home", "--shell", shell, "speedquality",
		); createErr != nil {
			return fmt.Errorf("创建 speedquality 系统用户失败: %s", strings.TrimSpace(string(output)))
		}
	}
	uidOutput, err := securityCommand("id", "-u", "speedquality")
	if err != nil {
		return errorsFromCommand("读取 speedquality UID", uidOutput, err)
	}
	gidOutput, err := securityCommand("id", "-g", "speedquality")
	if err != nil {
		return errorsFromCommand("读取 speedquality GID", gidOutput, err)
	}
	uid, uidErr := strconv.Atoi(strings.TrimSpace(string(uidOutput)))
	gid, gidErr := strconv.Atoi(strings.TrimSpace(string(gidOutput)))
	if uidErr != nil || gidErr != nil {
		return fmt.Errorf("speedquality 系统用户 ID 无效")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chown(directory, uid, gid); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	for _, item := range []string{path, statePath(path)} {
		if err := os.Chown(item, uid, gid); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Chmod(item, 0o600); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func errorsFromCommand(action string, output []byte, err error) error {
	message := strings.TrimSpace(string(bytes.TrimSpace(output)))
	if message == "" {
		message = err.Error()
	}
	return fmt.Errorf("%s失败: %s", action, message)
}
