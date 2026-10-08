package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const nodeProtocolVersion = 1

var nodeCapabilities = []string{"speed.http.v1"}

type registrationRequest struct {
	Version            int      `json:"version"`
	Challenge          string   `json:"challenge"`
	Region             string   `json:"region"`
	Carrier            string   `json:"carrier"`
	Label              string   `json:"label,omitempty"`
	IPv4               string   `json:"ipv4,omitempty"`
	IPv6               string   `json:"ipv6,omitempty"`
	Port               int      `json:"port"`
	MaxMbps            int      `json:"max_mbps"`
	MaxConcurrency     int      `json:"max_concurrency"`
	ReserveConcurrency int      `json:"reserve_concurrency"`
	DailyPublicBytes   int64    `json:"daily_public_bytes"`
	DailyTotalBytes    int64    `json:"daily_total_bytes"`
	AccessMode         string   `json:"access_mode"`
	Availability       string   `json:"availability"`
	Timezone           string   `json:"timezone"`
	Available          bool     `json:"available"`
	AgentVersion       string   `json:"agent_version"`
	ProtocolVersion    int      `json:"protocol_version"`
	Capabilities       []string `json:"capabilities"`
	FirewallReady      bool     `json:"firewall_ready"`
	Authorized         bool     `json:"authorized"`
}

type detectionResponse struct {
	Address        string `json:"address"`
	Family         string `json:"family"`
	CountryCode    string `json:"country_code"`
	Region         string `json:"region"`
	RegionName     string `json:"region_name"`
	Carrier        string `json:"carrier"`
	CarrierName    string `json:"carrier_name"`
	ASN            int    `json:"asn"`
	ASOrganization string `json:"as_organization"`
}

type detectedEnvironment struct {
	IPv4           string
	IPv6           string
	CountryCode    string
	Region         string
	RegionName     string
	Carrier        string
	CarrierName    string
	ASN            int
	ASOrganization string
}

type registrationResponse struct {
	NodeID        string    `json:"node_id"`
	RouteKey      string    `json:"route_key"`
	APIToken      string    `json:"api_token"`
	JWTPublicJWK  PublicJWK `json:"jwt_public_jwk"`
	JWTIssuer     string    `json:"jwt_issuer"`
	JWTAudience   string    `json:"jwt_audience"`
	HeartbeatSecs int       `json:"heartbeat_seconds"`
	Admission     string    `json:"admission_status"`
	CertifiedMbps int       `json:"certified_mbps"`
}

type heartbeatRequest struct {
	AccessMode         string   `json:"access_mode"`
	MaxMbps            int      `json:"max_mbps"`
	MaxConcurrency     int      `json:"max_concurrency"`
	ReserveConcurrency int      `json:"reserve_concurrency"`
	DailyPublicBytes   int64    `json:"daily_public_bytes"`
	DailyTotalBytes    int64    `json:"daily_total_bytes"`
	PublicBytesToday   int64    `json:"public_bytes_today"`
	PublicReserved     int64    `json:"public_reserved_bytes"`
	TotalBytesToday    int64    `json:"total_bytes_today"`
	TotalReserved      int64    `json:"total_reserved_bytes"`
	EmergencyStopped   bool     `json:"emergency_stopped"`
	Active             int      `json:"active"`
	PublicActive       int      `json:"public_active"`
	Availability       string   `json:"availability"`
	Timezone           string   `json:"timezone"`
	Available          bool     `json:"available"`
	AgentVersion       string   `json:"agent_version"`
	ProtocolVersion    int      `json:"protocol_version"`
	Capabilities       []string `json:"capabilities"`
	FirewallReady      bool     `json:"firewall_ready"`
}

type heartbeatResponse struct {
	Status        string    `json:"status"`
	JWTPublicJWK  PublicJWK `json:"jwt_public_jwk"`
	JWTIssuer     string    `json:"jwt_issuer"`
	JWTAudience   string    `json:"jwt_audience"`
	HeartbeatSecs int       `json:"heartbeat_seconds"`
	Admission     string    `json:"admission_status"`
	CertifiedMbps int       `json:"certified_mbps"`
}

type routeKeyResponse struct {
	Status   string `json:"status"`
	RouteKey string `json:"route_key,omitempty"`
}

var platformHTTPClient = &http.Client{Timeout: 35 * time.Second}
var nodeRespondsLocally = localNodeResponds
var detectNodeSetup = detectNodeEnvironment

func detectNodeEnvironment(platformURL string) (detectedEnvironment, error) {
	var detected detectedEnvironment
	var successful int
	for _, family := range []struct {
		network string
		name    string
	}{{network: "tcp4", name: "v4"}, {network: "tcp6", name: "v6"}} {
		value, err := detectNodeFamily(platformURL, family.network)
		if err != nil {
			continue
		}
		ip := net.ParseIP(value.Address)
		if value.Family != family.name || ip == nil || !publicIP(ip) {
			continue
		}
		successful++
		if family.name == "v4" {
			detected.IPv4 = value.Address
		} else {
			detected.IPv6 = value.Address
		}
		if detected.Region != "" && value.Region != "" && value.Region != detected.Region {
			if family.name == "v6" {
				detected.IPv6 = ""
			}
			continue
		}
		if detected.CountryCode == "" {
			detected.CountryCode = value.CountryCode
		}
		if detected.Region == "" {
			detected.Region = value.Region
			detected.RegionName = value.RegionName
		}
		if detected.Carrier == "" {
			detected.Carrier = value.Carrier
			detected.CarrierName = value.CarrierName
		}
		if detected.ASN == 0 {
			detected.ASN = value.ASN
			detected.ASOrganization = value.ASOrganization
		}
	}
	if successful == 0 || (detected.IPv4 == "" && detected.IPv6 == "") {
		return detectedEnvironment{}, errors.New("平台无法通过 IPv4 或 IPv6 识别当前公网地址")
	}
	return detected, nil
}

func detectNodeFamily(platformURL, network string) (detectionResponse, error) {
	dialer := &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 15 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, _, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}
	client := &http.Client{Timeout: 12 * time.Second, Transport: transport}
	request, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(platformURL, "/")+"/api/nodes/detect", nil)
	if err != nil {
		return detectionResponse{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "sq-node/"+version)
	response, err := client.Do(request)
	if err != nil {
		return detectionResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return detectionResponse{}, fmt.Errorf("节点环境检测返回 %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 32*1024+1))
	if err != nil || len(data) > 32*1024 {
		return detectionResponse{}, errors.New("节点环境检测响应无效")
	}
	var value detectionResponse
	if json.Unmarshal(data, &value) != nil {
		return detectionResponse{}, errors.New("节点环境检测响应格式无效")
	}
	return value, nil
}

func registerWithPlatform(path string) (Config, error) {
	config, err := loadConfig(path)
	if err != nil {
		return Config{}, err
	}
	if config.NodeID != "" || config.RouteKey != "" || config.APIToken != "" {
		return Config{}, errors.New("节点已经注册；如需更换身份信息，请先执行 sq-node unregister")
	}
	if config.PlatformURL == "" {
		return Config{}, errors.New("请先配置平台地址")
	}
	challenge, err := randomURLToken(32)
	if err != nil {
		return Config{}, err
	}
	config.OwnershipChallenge = "sq-owner-" + challenge
	if err := saveConfig(path, config); err != nil {
		return Config{}, err
	}
	ownershipChallenge := config.OwnershipChallenge
	challengeActive := true
	defer func() {
		if !challengeActive {
			return
		}
		current, loadErr := loadConfig(path)
		if loadErr == nil && current.OwnershipChallenge == ownershipChallenge {
			current.OwnershipChallenge = ""
			_ = saveConfig(path, current)
		}
	}()

	var temporary *nodeServer
	if !nodeRespondsLocally(config) {
		temporary, err = newNodeServer(path)
		if err != nil {
			return Config{}, err
		}
		listener, listenErr := listenForConfig(config)
		if listenErr != nil {
			return Config{}, errors.New("节点服务未运行，且无法临时启动所有权验证服务")
		}
		go func() { _ = temporary.serve(listener) }()
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = temporary.shutdown(ctx)
		}()
		time.Sleep(100 * time.Millisecond)
	}

	payload := registrationRequest{
		Version: 1, Challenge: config.OwnershipChallenge, Region: config.Region,
		Carrier: config.Carrier, Label: config.Label, IPv4: config.PublicIPv4,
		IPv6: config.PublicIPv6, Port: config.Port, MaxMbps: config.MaxMbps,
		MaxConcurrency: config.MaxConcurrency, ReserveConcurrency: config.ReserveConcurrency,
		DailyPublicBytes: config.DailyPublicBytes, DailyTotalBytes: config.DailyTotalBytes,
		AccessMode:   config.AccessMode,
		Availability: config.Availability, Timezone: config.Timezone,
		Available: availableAt(config, time.Now()), AgentVersion: version,
		ProtocolVersion: nodeProtocolVersion, Capabilities: append([]string(nil), nodeCapabilities...),
		FirewallReady: currentGateStatus(path, time.Now()).Ready,
		Authorized:    true,
	}
	var result registrationResponse
	if err := platformJSON(context.Background(), config, http.MethodPost, "/api/nodes/register", "", payload, &result); err != nil {
		return Config{}, err
	}
	if result.NodeID == "" || result.RouteKey == "" || result.APIToken == "" ||
		result.JWTIssuer == "" || result.JWTAudience == "" || result.JWTPublicJWK.X == "" {
		return Config{}, errors.New("平台返回的注册信息不完整")
	}
	config.NodeID = result.NodeID
	config.RouteKey = result.RouteKey
	config.APIToken = result.APIToken
	config.JWTPublicKey = result.JWTPublicJWK
	config.JWTIssuer = result.JWTIssuer
	config.JWTAudience = result.JWTAudience
	if result.HeartbeatSecs >= 30 && result.HeartbeatSecs <= 600 {
		config.HeartbeatSeconds = result.HeartbeatSecs
	}
	if result.Admission == "" {
		result.Admission = "observing"
	}
	if result.CertifiedMbps == 0 {
		result.CertifiedMbps = 100
	}
	config.AdmissionStatus = result.Admission
	config.CertifiedMbps = result.CertifiedMbps
	config.OwnershipChallenge = ""
	if err := saveConfig(path, config); err != nil {
		_ = platformJSON(
			context.Background(), config, http.MethodPost, "/api/nodes/unregister",
			result.APIToken, map[string]any{}, nil,
		)
		return Config{}, err
	}
	challengeActive = false
	return config, nil
}

func sendHeartbeat(ctx context.Context, path string, state *runtimeState) error {
	config, err := loadConfig(path)
	if err != nil {
		return err
	}
	if err := config.validate(true); err != nil {
		return err
	}
	payload := buildHeartbeatPayload(path, config, state, time.Now())
	var result heartbeatResponse
	if err := platformJSON(ctx, config, http.MethodPost, "/api/nodes/heartbeat", config.APIToken, payload, &result); err != nil {
		return err
	}
	changed := false
	if result.JWTPublicJWK.X != "" && result.JWTPublicJWK != config.JWTPublicKey {
		config.JWTPublicKey = result.JWTPublicJWK
		changed = true
	}
	if result.JWTIssuer != "" && result.JWTIssuer != config.JWTIssuer {
		config.JWTIssuer = result.JWTIssuer
		changed = true
	}
	if result.JWTAudience != "" && result.JWTAudience != config.JWTAudience {
		config.JWTAudience = result.JWTAudience
		changed = true
	}
	if result.HeartbeatSecs >= 30 && result.HeartbeatSecs <= 600 && result.HeartbeatSecs != config.HeartbeatSeconds {
		config.HeartbeatSeconds = result.HeartbeatSecs
		changed = true
	}
	if result.Admission != "" && result.Admission != config.AdmissionStatus {
		config.AdmissionStatus = result.Admission
		changed = true
	}
	if (result.CertifiedMbps == 100 || result.CertifiedMbps == 200 || result.CertifiedMbps == 400) &&
		result.CertifiedMbps != config.CertifiedMbps {
		config.CertifiedMbps = result.CertifiedMbps
		changed = true
	}
	if changed {
		return saveConfig(path, config)
	}
	return nil
}

func buildHeartbeatPayload(path string, config Config, state *runtimeState, now time.Time) heartbeatRequest {
	active, publicActive, publicBytes, publicReserved, totalBytes, totalReserved, emergencyStopped := state.snapshot(now)
	return heartbeatRequest{
		AccessMode: config.AccessMode, MaxMbps: config.MaxMbps,
		MaxConcurrency: config.MaxConcurrency, ReserveConcurrency: config.ReserveConcurrency,
		DailyPublicBytes: config.DailyPublicBytes, PublicBytesToday: publicBytes,
		PublicReserved: publicReserved, Active: active, PublicActive: publicActive,
		DailyTotalBytes: config.DailyTotalBytes, TotalBytesToday: totalBytes,
		TotalReserved: totalReserved, EmergencyStopped: emergencyStopped,
		Availability: config.Availability, Timezone: config.Timezone,
		Available: availableAt(config, now), AgentVersion: version,
		ProtocolVersion: nodeProtocolVersion, Capabilities: append([]string(nil), nodeCapabilities...),
		FirewallReady: currentGateStatus(path, now).Ready,
	}
}

func unregisterFromPlatform(path string) error {
	config, err := loadConfig(path)
	if err != nil {
		return err
	}
	if config.APIToken == "" {
		return errors.New("节点尚未注册")
	}
	if err := platformJSON(context.Background(), config, http.MethodPost, "/api/nodes/unregister", config.APIToken, map[string]any{}, nil); err != nil {
		return err
	}
	config.NodeID = ""
	config.RouteKey = ""
	config.APIToken = ""
	config.JWTIssuer = ""
	config.JWTPublicKey = PublicJWK{}
	config.AdmissionStatus = ""
	config.CertifiedMbps = 0
	return saveConfig(path, config)
}

func updatePlatformRouteKey(path, action string) (string, error) {
	config, err := loadConfig(path)
	if err != nil {
		return "", err
	}
	if config.APIToken == "" {
		return "", errors.New("节点尚未注册")
	}
	if action != "rotate" && action != "revoke" {
		return "", errors.New("Route Key 操作必须是 rotate 或 revoke")
	}
	var result routeKeyResponse
	if err := platformJSON(
		context.Background(), config, http.MethodPost, "/api/nodes/route-key",
		config.APIToken, map[string]string{"action": action}, &result,
	); err != nil {
		return "", err
	}
	if action == "rotate" {
		if result.Status != "rotated" || !routeKeyPattern.MatchString(result.RouteKey) {
			return "", errors.New("平台返回的 Route Key 无效")
		}
		config.RouteKey = result.RouteKey
	} else {
		if result.Status != "revoked" {
			return "", errors.New("平台未确认撤销 Route Key")
		}
		config.RouteKey = ""
	}
	if err := saveConfig(path, config); err != nil {
		return "", err
	}
	return config.RouteKey, nil
}

func platformJSON(ctx context.Context, config Config, method, endpoint, token string, input any, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	requestContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestContext, method, config.PlatformURL+endpoint, bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "sq-node/"+version)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := platformHTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("平台请求失败: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 128*1024+1))
	if err != nil {
		return err
	}
	if len(data) > 128*1024 {
		return errors.New("平台响应过大")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := strings.TrimSpace(string(data))
		var errorBody struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &errorBody) == nil && errorBody.Error != "" {
			message = errorBody.Error
		}
		if message == "" {
			message = response.Status
		}
		return fmt.Errorf("平台拒绝请求: %s", message)
	}
	if output == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, output); err != nil {
		return errors.New("平台响应格式无效")
	}
	return nil
}

func localNodeResponds(config Config) bool {
	addresses := []string{"127.0.0.1"}
	if strings.Contains(config.ListenAddress, ":") {
		addresses = append(addresses, "::1")
	}
	client := &http.Client{Timeout: 800 * time.Millisecond}
	for _, address := range addresses {
		url := "http://" + net.JoinHostPort(address, fmt.Sprintf("%d", config.Port)) + "/healthz"
		response, err := client.Get(url)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return true
			}
		}
	}
	return false
}
