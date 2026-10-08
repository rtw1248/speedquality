package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var releasePublicKeyBase64 string

var updateHTTPClient = &http.Client{Timeout: 90 * time.Second}
var updateExecutablePath = os.Executable
var updateSystemctl = func(arguments ...string) ([]byte, error) {
	return exec.Command("systemctl", arguments...).CombinedOutput()
}
var updateSleep = time.Sleep
var updateNodeResponds = localNodeResponds
var updateServiceInstalled = func(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

type nodeUpdateInfo struct {
	Channel      string `json:"channel"`
	Version      string `json:"version"`
	ManifestURL  string `json:"manifest_url"`
	SignatureURL string `json:"signature_url"`
}

type releaseAsset struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type releaseManifest struct {
	Version string                  `json:"version"`
	Assets  map[string]releaseAsset `json:"assets"`
}

type verifiedUpdate struct {
	info     nodeUpdateInfo
	manifest releaseManifest
	asset    string
	details  releaseAsset
}

func updateCommand(path string, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(output)
	checkOnly := flags.Bool("check", false, "只检查更新")
	automatic := flags.Bool("automatic", false, "由 systemd 定时器调用")
	mode := flags.String("mode", "", "automatic 或 manual")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	config, err := loadConfig(path)
	if err != nil {
		return err
	}
	if *mode != "" {
		config.UpdateMode = strings.ToLower(strings.TrimSpace(*mode))
		if err := saveConfig(path, config); err != nil {
			return err
		}
		fmt.Fprintf(output, "更新模式已设置为 %s。\n", config.UpdateMode)
		return nil
	}
	if *automatic && config.UpdateMode != "automatic" {
		return nil
	}
	update, err := checkNodeUpdate(config)
	if err != nil {
		return err
	}
	comparison, err := compareReleaseVersions(version, update.manifest.Version)
	if err != nil {
		return err
	}
	if comparison >= 0 {
		if !*automatic {
			fmt.Fprintf(output, "当前已是最新稳定版本：%s\n", version)
		}
		return nil
	}
	if *checkOnly {
		fmt.Fprintf(output, "发现新版本：%s（当前 %s）\n", update.manifest.Version, version)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("更新 /usr/local/bin/sq-node 需要 root 权限，请使用 sudo sq-node update")
	}
	if err := installNodeUpdate(path, config, update); err != nil {
		return err
	}
	fmt.Fprintf(output, "sq-node 已更新到 %s。\n", update.manifest.Version)
	return nil
}

func checkNodeUpdate(config Config) (verifiedUpdate, error) {
	if config.PlatformURL == "" {
		return verifiedUpdate{}, errors.New("尚未配置 SpeedQuality 平台地址")
	}
	request, err := http.NewRequest(
		http.MethodGet,
		config.PlatformURL+"/api/nodes/update?channel="+config.UpdateChannel,
		nil,
	)
	if err != nil {
		return verifiedUpdate{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "sq-node/"+version)
	response, err := updateHTTPClient.Do(request)
	if err != nil {
		return verifiedUpdate{}, fmt.Errorf("检查更新失败: %w", err)
	}
	data, err := readUpdateResponse(response, 32*1024)
	if err != nil {
		return verifiedUpdate{}, err
	}
	var info nodeUpdateInfo
	if json.Unmarshal(data, &info) != nil || info.Channel != "stable" || !validReleaseVersion(info.Version) ||
		!samePlatformURL(config.PlatformURL, info.ManifestURL) || !samePlatformURL(config.PlatformURL, info.SignatureURL) {
		return verifiedUpdate{}, errors.New("平台返回的更新信息无效")
	}
	manifestBytes, err := fetchUpdateBytes(info.ManifestURL, 128*1024)
	if err != nil {
		return verifiedUpdate{}, err
	}
	signatureText, err := fetchUpdateBytes(info.SignatureURL, 4096)
	if err != nil {
		return verifiedUpdate{}, err
	}
	if err := verifyReleaseManifest(manifestBytes, signatureText); err != nil {
		return verifiedUpdate{}, err
	}
	var manifest releaseManifest
	decoder := json.NewDecoder(strings.NewReader(string(manifestBytes)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || manifest.Version != info.Version || !validReleaseVersion(manifest.Version) {
		return verifiedUpdate{}, errors.New("发布清单格式无效")
	}
	asset := "sq-node-linux-" + runtime.GOARCH
	details, exists := manifest.Assets[asset]
	if !exists || len(manifest.Assets) > 16 || len(details.SHA256) != 64 || details.Size < 1 ||
		details.Size > 64*1024*1024 {
		return verifiedUpdate{}, errors.New("发布清单不包含当前系统的有效 sq-node")
	}
	if _, err := hex.DecodeString(details.SHA256); err != nil {
		return verifiedUpdate{}, errors.New("发布清单中的 SHA-256 无效")
	}
	return verifiedUpdate{info: info, manifest: manifest, asset: asset, details: details}, nil
}

func readUpdateResponse(response *http.Response, maximum int64) ([]byte, error) {
	if response == nil {
		return nil, errors.New("更新服务没有响应")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("更新服务返回 %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, errors.New("更新服务响应过大")
	}
	return data, nil
}

func fetchUpdateBytes(url string, maximum int64) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "sq-node/"+version)
	response, err := updateHTTPClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("下载更新文件失败: %w", err)
	}
	return readUpdateResponse(response, maximum)
}

func verifyReleaseManifest(manifest, signatureText []byte) error {
	publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(releasePublicKeyBase64))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("sq-node 未配置有效的发布签名公钥")
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signatureText)))
	if err != nil || len(signature) != ed25519.SignatureSize ||
		!ed25519.Verify(ed25519.PublicKey(publicKey), manifest, signature) {
		return errors.New("发布清单签名验证失败")
	}
	return nil
}

func samePlatformURL(platform, candidate string) bool {
	base, baseErr := http.NewRequest(http.MethodGet, platform, nil)
	target, targetErr := http.NewRequest(http.MethodGet, candidate, nil)
	return baseErr == nil && targetErr == nil && target.URL.Scheme == "https" &&
		base.URL.Scheme == target.URL.Scheme && target.URL.User == nil &&
		strings.EqualFold(base.URL.Host, target.URL.Host)
}

func validReleaseVersion(value string) bool {
	if len(value) < 6 || value[0] != 'v' {
		return false
	}
	_, err := releaseVersionParts(value)
	return err == nil
}

func releaseVersionParts(value string) ([3]int, error) {
	var result [3]int
	parts := strings.Split(strings.TrimPrefix(value, "v"), ".")
	if len(parts) != 3 {
		return result, errors.New("版本号必须是 vX.Y.Z")
	}
	for index, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 || parsed > 1_000_000 || (len(part) > 1 && part[0] == '0') {
			return result, errors.New("版本号无效")
		}
		result[index] = parsed
	}
	return result, nil
}

func compareReleaseVersions(current, available string) (int, error) {
	if current == "dev" {
		return -1, nil
	}
	left, err := releaseVersionParts(current)
	if err != nil {
		return 0, fmt.Errorf("当前版本号无效: %w", err)
	}
	right, err := releaseVersionParts(available)
	if err != nil {
		return 0, err
	}
	for index := range left {
		if left[index] < right[index] {
			return -1, nil
		}
		if left[index] > right[index] {
			return 1, nil
		}
	}
	return 0, nil
}

func installNodeUpdate(path string, config Config, update verifiedUpdate) error {
	executable, err := updateExecutablePath()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	binaryURL := fmt.Sprintf("%s/bin/%s/%s", config.PlatformURL, update.manifest.Version, update.asset)
	binary, err := fetchUpdateBytes(binaryURL, update.details.Size)
	if err != nil {
		return err
	}
	if int64(len(binary)) != update.details.Size {
		return errors.New("下载的 sq-node 大小与发布清单不一致")
	}
	digest := sha256.Sum256(binary)
	if hex.EncodeToString(digest[:]) != strings.ToLower(update.details.SHA256) {
		return errors.New("下载的 sq-node SHA-256 校验失败")
	}
	temporary, err := os.CreateTemp(filepath.Dir(executable), ".sq-node-update-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(binary); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o755); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	nodeManaged := updateServiceInstalled(nodeServiceUnitPath)
	gateManaged := updateServiceInstalled(gateServiceUnitPath)
	if gateManaged {
		if err := runSystemctl("stop", "speedquality-node-gate.service"); err != nil {
			return fmt.Errorf("停止节点网关失败，未安装更新: %w", err)
		}
	}
	backup := executable + ".previous"
	_ = os.Remove(backup)
	if err := os.Rename(executable, backup); err != nil {
		if gateManaged {
			_, _ = updateSystemctl("restart", "speedquality-node-gate.service")
		}
		return err
	}
	if err := os.Rename(temporaryPath, executable); err != nil {
		_ = os.Rename(backup, executable)
		if gateManaged {
			_, _ = updateSystemctl("restart", "speedquality-node-gate.service")
		}
		return err
	}
	if !nodeManaged {
		if gateManaged {
			_, _ = updateSystemctl("restart", "speedquality-node-gate.service")
		}
		return nil
	}
	if err := restartUpdatedNode(config, gateManaged); err != nil {
		rollbackErr := rollbackNodeUpdate(executable, backup, config, gateManaged)
		if rollbackErr != nil {
			return fmt.Errorf("新版本启动失败: %w；回滚未完整完成: %v", err, rollbackErr)
		}
		return fmt.Errorf("新版本启动失败，已回滚: %w", err)
	}
	return nil
}

func restartUpdatedNode(config Config, gateManaged bool) error {
	if err := runSystemctl("restart", "speedquality-node.service"); err != nil {
		return fmt.Errorf("重启节点服务失败: %w", err)
	}
	if err := waitForUpdatedNode(config); err != nil {
		return err
	}
	if gateManaged {
		if err := runSystemctl("restart", "speedquality-node-gate.service"); err != nil {
			return fmt.Errorf("重启节点网关失败: %w", err)
		}
	}
	return nil
}

func waitForUpdatedNode(config Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		if updateNodeResponds(config) {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("节点服务未在 20 秒内恢复")
		default:
			updateSleep(500 * time.Millisecond)
		}
	}
}

func rollbackNodeUpdate(executable, backup string, config Config, gateManaged bool) error {
	var rollbackErrors []error
	if gateManaged {
		if err := runSystemctl("stop", "speedquality-node-gate.service"); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("停止新网关: %w", err))
		}
	}
	if err := os.Rename(backup, executable); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("恢复旧二进制: %w", err))
	}
	if err := restartUpdatedNode(config, gateManaged); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("恢复旧服务: %w", err))
	}
	return errors.Join(rollbackErrors...)
}
