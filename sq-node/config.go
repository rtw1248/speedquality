package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const configVersion = 1

var (
	nodeIDPattern    = regexp.MustCompile(`^[a-f0-9]{32}$`)
	routeKeyPattern  = regexp.MustCompile(`^sqn_[A-Za-z0-9_-]{24,96}$`)
	apiTokenPattern  = regexp.MustCompile(`^sqa_[A-Za-z0-9_-]{24,128}$`)
	challengePattern = regexp.MustCompile(`^sq-owner-[A-Za-z0-9_-]{32,128}$`)
)

var portAvailable = func(port int) bool {
	listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

type PublicJWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Alg string `json:"alg,omitempty"`
	Kid string `json:"kid,omitempty"`
}

type Config struct {
	Version               int       `json:"version"`
	PlatformURL           string    `json:"platform_url"`
	ListenAddress         string    `json:"listen_address"`
	Port                  int       `json:"port"`
	PublicIPv4            string    `json:"public_ipv4,omitempty"`
	PublicIPv6            string    `json:"public_ipv6,omitempty"`
	Region                string    `json:"region"`
	Carrier               string    `json:"carrier"`
	Label                 string    `json:"label,omitempty"`
	MaxMbps               int       `json:"max_mbps"`
	MaxConcurrency        int       `json:"max_concurrency"`
	ReserveConcurrency    int       `json:"reserve_concurrency"`
	DailyPublicBytes      int64     `json:"daily_public_bytes"`
	DailyTotalBytes       int64     `json:"daily_total_bytes"`
	AccessMode            string    `json:"access_mode"`
	Availability          string    `json:"availability"`
	Timezone              string    `json:"timezone"`
	UpdateMode            string    `json:"update_mode"`
	UpdateChannel         string    `json:"update_channel"`
	NodeID                string    `json:"node_id,omitempty"`
	RouteKey              string    `json:"route_key,omitempty"`
	APIToken              string    `json:"api_token,omitempty"`
	OwnershipChallenge    string    `json:"ownership_challenge,omitempty"`
	JWTPublicKey          PublicJWK `json:"jwt_public_jwk,omitempty"`
	JWTIssuer             string    `json:"jwt_issuer,omitempty"`
	JWTAudience           string    `json:"jwt_audience,omitempty"`
	HeartbeatSeconds      int       `json:"heartbeat_seconds"`
	AdmissionStatus       string    `json:"admission_status,omitempty"`
	CertifiedMbps         int       `json:"certified_mbps,omitempty"`
	AllowRegistrationFrom string    `json:"allow_registration_from,omitempty"`
}

func defaultConfig() Config {
	return Config{
		Version:            configVersion,
		ListenAddress:      "::",
		MaxMbps:            100,
		MaxConcurrency:     1,
		ReserveConcurrency: 0,
		DailyPublicBytes:   10_000_000_000,
		DailyTotalBytes:    20_000_000_000,
		AccessMode:         "public",
		Availability:       "always",
		Timezone:           "Local",
		UpdateMode:         "automatic",
		UpdateChannel:      "stable",
		JWTAudience:        "sq-node",
		HeartbeatSeconds:   60,
	}
}

func configPath() string {
	if value := strings.TrimSpace(os.Getenv("SQ_NODE_CONFIG")); value != "" {
		return value
	}
	if os.Geteuid() == 0 {
		return "/etc/speedquality/node.json"
	}
	directory, err := os.UserConfigDir()
	if err != nil || directory == "" {
		directory = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(directory, "speedquality", "node.json")
}

func statePath(path string) string {
	return filepath.Join(filepath.Dir(path), "node-state.json")
}

func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if len(data) > 128*1024 {
		return Config{}, errors.New("配置文件超过 128 KiB")
	}
	var config Config
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("配置文件无效: %w", err)
	}
	applyConfigDefaults(&config)
	if err := config.validate(false); err != nil {
		return Config{}, err
	}
	return config, nil
}

func applyConfigDefaults(config *Config) {
	if config.DailyTotalBytes == 0 {
		config.DailyTotalBytes = 20_000_000_000
		if config.DailyPublicBytes > config.DailyTotalBytes {
			config.DailyTotalBytes = config.DailyPublicBytes
		}
	}
	if config.Availability == "" {
		config.Availability = "always"
	}
	if config.Timezone == "" {
		config.Timezone = "Local"
	}
	if config.UpdateMode == "" {
		config.UpdateMode = "automatic"
	}
	if config.UpdateChannel == "" {
		config.UpdateChannel = "stable"
	}
}

func saveConfig(path string, config Config) error {
	applyConfigDefaults(&config)
	if err := config.validate(false); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (config Config) validate(requireRegistration bool) error {
	if config.Version != configVersion {
		return fmt.Errorf("不支持的配置版本: %d", config.Version)
	}
	if config.Port < 1 || config.Port > 65535 {
		return errors.New("服务端口无效")
	}
	if config.ListenAddress == "" || strings.ContainsAny(config.ListenAddress, "\r\n") {
		return errors.New("监听地址无效")
	}
	if config.PublicIPv4 == "" && config.PublicIPv6 == "" {
		return errors.New("至少需要一个公网 IP")
	}
	if config.PublicIPv4 != "" {
		ip := net.ParseIP(config.PublicIPv4)
		if ip == nil || ip.To4() == nil || !publicIP(ip) {
			return errors.New("公网 IPv4 无效")
		}
	}
	if config.PublicIPv6 != "" {
		ip := net.ParseIP(config.PublicIPv6)
		if ip == nil || ip.To4() != nil || !publicIP(ip) {
			return errors.New("公网 IPv6 无效")
		}
	}
	if !validRegion(config.Region) {
		return errors.New("省份代码无效")
	}
	if !validCarrier(config.Carrier) {
		return errors.New("运营商只支持 ct、cu 或 cm")
	}
	if config.MaxMbps != 100 && config.MaxMbps != 200 && config.MaxMbps != 400 {
		return errors.New("最高档位只支持 100、200 或 400 Mbps")
	}
	if config.MaxConcurrency < 1 || config.MaxConcurrency > 20 {
		return errors.New("最大并发必须在 1 到 20 之间")
	}
	if config.ReserveConcurrency < 0 || config.ReserveConcurrency >= config.MaxConcurrency {
		return errors.New("保留并发必须小于最大并发")
	}
	if config.DailyPublicBytes < 0 || config.DailyPublicBytes > 10_000_000_000_000 {
		return errors.New("每日公共流量上限无效")
	}
	if config.DailyTotalBytes < 1_000_000 || config.DailyTotalBytes > 10_000_000_000_000 ||
		config.DailyTotalBytes < config.DailyPublicBytes {
		return errors.New("每日总流量上限必须不小于公共额度，且在 1 MB 到 10 TB 之间")
	}
	if config.AccessMode != "public" && config.AccessMode != "private" && config.AccessMode != "paused" {
		return errors.New("访问模式必须是 public、private 或 paused")
	}
	if _, err := parseAvailability(config.Availability); err != nil {
		return err
	}
	if _, err := configLocation(config.Timezone); err != nil {
		return err
	}
	if config.UpdateMode != "automatic" && config.UpdateMode != "manual" {
		return errors.New("更新模式必须是 automatic 或 manual")
	}
	if config.UpdateChannel != "stable" {
		return errors.New("当前只支持 stable 更新通道")
	}
	if len(config.Label) > 48 || strings.ContainsAny(config.Label, "\r\n") {
		return errors.New("节点标签无效")
	}
	if config.PlatformURL != "" {
		parsed, err := url.Parse(config.PlatformURL)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
			parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("平台地址必须是没有尾部斜杠的 HTTPS URL")
		}
	}
	if config.HeartbeatSeconds < 30 || config.HeartbeatSeconds > 600 {
		return errors.New("心跳间隔必须在 30 到 600 秒之间")
	}
	if config.AdmissionStatus != "" && config.AdmissionStatus != "observing" &&
		config.AdmissionStatus != "active" && config.AdmissionStatus != "suspended" {
		return errors.New("平台准入状态无效")
	}
	if config.CertifiedMbps != 0 && config.CertifiedMbps != 100 &&
		config.CertifiedMbps != 200 && config.CertifiedMbps != 400 {
		return errors.New("平台认证档位无效")
	}
	if config.OwnershipChallenge != "" && !challengePattern.MatchString(config.OwnershipChallenge) {
		return errors.New("所有权验证 challenge 无效")
	}
	registrationPresent := config.NodeID != "" || config.RouteKey != "" || config.APIToken != ""
	if registrationPresent {
		if !nodeIDPattern.MatchString(config.NodeID) ||
			(config.RouteKey != "" && !routeKeyPattern.MatchString(config.RouteKey)) ||
			!apiTokenPattern.MatchString(config.APIToken) || config.JWTIssuer == "" ||
			len(config.JWTIssuer) > 256 || strings.ContainsAny(config.JWTIssuer, "\r\n\x00") ||
			config.JWTAudience == "" || len(config.JWTAudience) > 128 ||
			strings.ContainsAny(config.JWTAudience, "\r\n\x00") {
			return errors.New("平台注册凭据无效")
		}
		if _, err := publicKeyFromJWK(config.JWTPublicKey); err != nil {
			return err
		}
	}
	if requireRegistration {
		if !registrationPresent {
			return errors.New("节点尚未注册")
		}
	}
	return nil
}

func publicIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return false
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		if ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127 {
			return false
		}
		if ipv4[0] == 198 && (ipv4[1] == 18 || ipv4[1] == 19) {
			return false
		}
		if (ipv4[0] == 192 && ipv4[1] == 0 && (ipv4[2] == 0 || ipv4[2] == 2)) ||
			(ipv4[0] == 198 && ipv4[1] == 51 && ipv4[2] == 100) ||
			(ipv4[0] == 203 && ipv4[1] == 0 && ipv4[2] == 113) {
			return false
		}
		return true
	}
	return !strings.HasPrefix(strings.ToLower(ip.String()), "2001:db8:")
}

func randomPort() (int, error) {
	for attempt := 0; attempt < 80; attempt++ {
		buffer := make([]byte, 2)
		if _, err := rand.Read(buffer); err != nil {
			return 0, err
		}
		port := 50000 + (int(buffer[0])<<8|int(buffer[1]))%10000
		if portAvailable(port) {
			return port, nil
		}
	}
	return 0, errors.New("无法在 50000-59999 中找到空闲端口")
}

func listenHost(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		return strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
	}
	return value
}

func randomURLToken(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func validCarrier(value string) bool {
	return value == "ct" || value == "cu" || value == "cm"
}

var carrierNames = map[string]string{"ct": "电信", "cu": "联通", "cm": "移动"}

func validRegion(value string) bool {
	_, exists := regionNames[value]
	return exists
}

var regionNames = map[string]string{
	"bj": "北京", "tj": "天津", "he": "河北", "sx": "山西", "nm": "内蒙古",
	"ln": "辽宁", "jl": "吉林", "hl": "黑龙江", "sh": "上海", "js": "江苏",
	"zj": "浙江", "ah": "安徽", "fj": "福建", "jx": "江西", "sd": "山东",
	"ha": "河南", "hb": "湖北", "hn": "湖南", "gd": "广东", "gx": "广西",
	"hi": "海南", "cq": "重庆", "sc": "四川", "gz": "贵州", "yn": "云南",
	"xz": "西藏", "sn": "陕西", "gs": "甘肃", "qh": "青海", "nx": "宁夏",
	"xj": "新疆", "tw": "台湾", "hk": "香港", "mo": "澳门",
}
