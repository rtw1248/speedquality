package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

const (
	nqMaxIPInfoBytes      = 1 * 1024 * 1024
	nqMaxRecordBytes      = 72 * 1024 * 1024
	nqMaxArchiveBytes     = 50 * 1024 * 1024
	nqMaxUncompressed     = 100 * 1024 * 1024
	nqMaxMemberBytes      = 10 * 1024 * 1024
	nqMaxMembers          = 256
	nqMaxSnapshotFiles    = 24
	nqMaxSnapshotFileText = 96 * 1024
	nqMaxSnapshotText     = 192 * 1024
)

var (
	nqSGRPattern  = regexp.MustCompile("\\x1b\\[[0-9;]*m")
	nqANSIPattern = regexp.MustCompile(
		"\\x1b\\[[0-?]*[ -/]*[@-~]",
	)
	nqOSCPattern     = regexp.MustCompile("\\x1b\\][^\\x07\\x1b]*(?:\\x07|\\x1b\\\\)")
	nqControlPattern = regexp.MustCompile(
		"[\\x00-\\x08\\x0b\\x0c\\x0e-\\x1f\\x7f-\\x9f]",
	)
	nqBidiPattern       = regexp.MustCompile("[\u202a-\u202e\u2066-\u2069]")
	nqPrivateUsePattern = regexp.MustCompile("[\ue000-\uf8ff]")
	nqReportTimePattern = regexp.MustCompile(
		`(?i)(?:报告|检测|测试)(?:生成)?时间[[:space:]]*[:：][[:space:]]*` +
			`([0-9]{4}[-/.][0-9]{1,2}[-/.][0-9]{1,2}[ T][0-9]{1,2}:[0-9]{2}` +
			`(?::[0-9]{2})?(?:\.[0-9]+)?)[[:space:]]*` +
			`(Z|(?:UTC|GMT)(?:[[:space:]]*[+-][[:space:]]*[0-9]{1,2}(?::?[0-9]{2})?)?` +
			`|CST|北京时间|[+-][0-9]{2}:?[0-9]{2})?`,
	)
	nqISOTimePattern = regexp.MustCompile(
		`(?i)([0-9]{4}-[0-9]{1,2}-[0-9]{1,2}[ T][0-9]{1,2}:[0-9]{2}` +
			`(?::[0-9]{2})?(?:\.[0-9]+)?)[[:space:]]*(Z|[+-][0-9]{2}:?[0-9]{2})?`,
	)
	nqDatePattern = regexp.MustCompile(
		`^([0-9]{4})[-/.]([0-9]{1,2})[-/.]([0-9]{1,2})[ T]` +
			`([0-9]{1,2}):([0-9]{2})(?::([0-9]{2}))?(?:\.([0-9]+))?$`,
	)
	nqImagePattern = regexp.MustCompile(
		`(?i)https://[^[:space:]<>()\]"']+\.(?:png|jpe?g|webp|gif|svg)` +
			`(?:\?[^[:space:]<>()\]"']*)?`,
	)
	nqTabItemPattern   = regexp.MustCompile(`^:::[[:space:]]+tab-item[[:space:]]+(.+?)[[:space:]]*$`)
	nqANSIFencePattern = regexp.MustCompile(
		`(?is)` + "```ansi[[:space:]]*\\n(.*?)\\n```",
	)
	nqLeadingTitlePattern = regexp.MustCompile(`^[?[:space:]]+`)
	nqSlugPattern         = regexp.MustCompile(`[^a-z0-9]+`)
	nqDigitsPattern       = regexp.MustCompile(`[0-9]+`)
	nqNumericTimePattern  = regexp.MustCompile(`^[0-9]{10,13}$`)
	nqJSONIPPattern       = regexp.MustCompile(
		`(?i)"IP"[[:space:]]*:[[:space:]]*"([0-9A-Fa-f:.*]+)"`,
	)
	nqIdentityPattern = regexp.MustCompile(
		`(?i)IP质量体检报告[[:space:]]*[:：][[:space:]]*([0-9A-Fa-f:.*]+)`,
	)
	nqIPv4Pattern = regexp.MustCompile(
		`(?:^|[^0-9])((?:[0-9]{1,3}\.){2}(?:[0-9]{1,3}|\*)\.(?:[0-9]{1,3}|\*))(?:[^0-9]|$)`,
	)
	nqPartialSGRPattern = regexp.MustCompile("\\x1b\\[[0-9;]*$")
)

var nqTimeKeys = map[string]bool{
	"reporttime": true, "reportdate": true, "createdat": true, "createtime": true,
	"generatedat": true, "generatedtime": true, "testtime": true, "testedat": true,
	"completedat": true, "finishtime": true,
}

type nodeQualityPage struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Format    string `json:"format"`
	Content   string `json:"content"`
	ImageURL  string `json:"image_url"`
	Source    string `json:"source"`
	Truncated bool   `json:"truncated"`
}

type nodeQualitySnapshot struct {
	Version      int               `json:"version"`
	Pages        []nodeQualityPage `json:"pages"`
	Truncated    bool              `json:"truncated"`
	OmittedFiles int               `json:"omitted_files"`
}

type nodeQualityAnalysis struct {
	Status           string
	CurrentIP        string
	CurrentASN       string
	ReportIPs        []string
	ReportASN        string
	Reason           string
	TimeStatus       string
	ReportTime       string
	TimeGapSeconds   string
	ReportEpoch      string
	ReportTimeSource string
	Snapshot         nodeQualitySnapshot
}

type nodeQualityTimeCandidate struct {
	Time   time.Time
	Source string
}

func runNodeQualityCommand(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("nq-verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	ipInfoPath := flags.String("ipinfo", "", "NodeQuality 当前服务器信息 JSON")
	recordPath := flags.String("record", "", "NodeQuality 报告 API JSON")
	testedAt := flags.Int64("tested-at", 0, "本次测速完成时间 Unix 秒")
	maxTimeGap := flags.Int("max-time-gap", 60, "允许的最大时间差，单位分钟")
	snapshotPath := flags.String("snapshot", "", "安全分页快照输出路径")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("nq-verify 不接受位置参数")
	}
	if *ipInfoPath == "" || *recordPath == "" || *snapshotPath == "" {
		return errors.New("--ipinfo、--record 和 --snapshot 均为必填参数")
	}
	if *testedAt <= 0 {
		return errors.New("--tested-at 必须是有效的 Unix 秒")
	}
	if *maxTimeGap < 1 || *maxTimeGap > 24*60 {
		return errors.New("--max-time-gap 必须在 1 到 1440 分钟之间")
	}
	analysis, err := verifyNodeQuality(
		*ipInfoPath, *recordPath, *testedAt, *maxTimeGap,
	)
	if err != nil {
		return err
	}
	if analysis.Status == "match" && len(analysis.Snapshot.Pages) > 0 {
		if err := writeNodeQualitySnapshot(*snapshotPath, analysis.Snapshot); err != nil {
			return fmt.Errorf("无法写入 NodeQuality 快照: %w", err)
		}
	}
	_, err = fmt.Fprintln(stdout, analysis.shellLine())
	return err
}

func (analysis nodeQualityAnalysis) shellLine() string {
	fields := []string{
		analysis.Status,
		analysis.CurrentIP,
		analysis.CurrentASN,
		strings.Join(analysis.ReportIPs, ","),
		analysis.ReportASN,
		analysis.Reason,
		analysis.TimeStatus,
		analysis.ReportTime,
		analysis.TimeGapSeconds,
		analysis.ReportEpoch,
		analysis.ReportTimeSource,
	}
	for index := range fields {
		fields[index] = strings.ReplaceAll(fields[index], "|", "")
	}
	return strings.Join(fields, "|")
}

func verifyNodeQuality(
	ipInfoPath string,
	recordPath string,
	testedAt int64,
	maxTimeGapMinutes int,
) (nodeQualityAnalysis, error) {
	currentValue, err := readNodeQualityJSON(ipInfoPath, nqMaxIPInfoBytes)
	if err != nil {
		return nodeQualityAnalysis{}, fmt.Errorf("当前服务器信息无效: %w", err)
	}
	responseValue, err := readNodeQualityJSON(recordPath, nqMaxRecordBytes)
	if err != nil {
		return nodeQualityAnalysis{}, fmt.Errorf("NodeQuality 报告响应无效: %w", err)
	}
	current, ok := currentValue.(map[string]any)
	if !ok {
		return nodeQualityAnalysis{}, errors.New("当前服务器信息必须是 JSON 对象")
	}
	response, ok := responseValue.(map[string]any)
	if !ok {
		return nodeQualityAnalysis{}, errors.New("NodeQuality 报告响应必须是 JSON 对象")
	}
	success, _ := response["success"].(bool)
	data, ok := response["data"].(map[string]any)
	if !success || !ok {
		return nodeQualityAnalysis{}, errors.New("NodeQuality API 没有返回可用报告")
	}
	encoded, ok := data["result"].(string)
	if !ok || encoded == "" {
		return nodeQualityAnalysis{}, errors.New("NodeQuality 报告缺少压缩内容")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(nqMaxArchiveBytes) {
		return nodeQualityAnalysis{}, errors.New("NodeQuality 报告压缩内容超过安全限制")
	}
	archive, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nodeQualityAnalysis{}, errors.New("NodeQuality 报告不是有效的 Base64")
	}
	if len(archive) == 0 || len(archive) > nqMaxArchiveBytes {
		return nodeQualityAnalysis{}, errors.New("NodeQuality 报告压缩内容大小无效")
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nodeQualityAnalysis{}, fmt.Errorf("NodeQuality 报告不是有效的 ZIP: %w", err)
	}
	if len(reader.File) > nqMaxMembers {
		return nodeQualityAnalysis{}, errors.New("NodeQuality 报告 ZIP 成员过多")
	}
	var total uint64
	for _, member := range reader.File {
		if member.UncompressedSize64 > nqMaxUncompressed-total {
			return nodeQualityAnalysis{}, errors.New("NodeQuality 报告解压后超过安全限制")
		}
		total += member.UncompressedSize64
	}

	currentIP := stringValue(current["ip"])
	currentASN := cleanASN(current["asn"])
	reportASN := cleanASN(data["asn"])
	reportIPs, err := findNodeQualityReportIPs(reader.File)
	if err != nil {
		return nodeQualityAnalysis{}, err
	}
	analysis := nodeQualityAnalysis{
		CurrentIP: currentIP, CurrentASN: currentASN, ReportIPs: reportIPs, ReportASN: reportASN,
	}
	var reasons []string
	for _, candidate := range reportIPs {
		matched, reason := nodeQualityIdentityMatches(currentIP, candidate, currentASN, reportASN)
		reasons = append(reasons, reason)
		if matched {
			analysis.Status = "match"
			analysis.Reason = reason
			break
		}
	}
	if len(reportIPs) == 0 {
		analysis.Status = "unverified"
		analysis.Reason = "report_has_no_ip_identity"
	} else if analysis.Status != "match" {
		analysis.Status = "mismatch"
		analysis.Reason = "identity_differs"
		if len(reasons) > 0 {
			analysis.Reason = reasons[0]
		}
	}

	reportTime, err := findNodeQualityReportTime(reader.File, responseValue)
	if err != nil {
		return nodeQualityAnalysis{}, err
	}
	if !reportTime.Time.IsZero() {
		china := time.FixedZone("CST", 8*60*60)
		analysis.ReportTime = reportTime.Time.In(china).Format("2006-01-02 15:04:05 MST")
		analysis.ReportEpoch = strconv.FormatInt(reportTime.Time.Unix(), 10)
		analysis.ReportTimeSource = reportTime.Source
		gap := testedAt - reportTime.Time.Unix()
		if gap < 0 {
			gap = -gap
		}
		analysis.TimeGapSeconds = strconv.FormatInt(gap, 10)
		if gap <= int64(maxTimeGapMinutes)*60 {
			analysis.TimeStatus = "close"
		} else {
			analysis.TimeStatus = "stale"
		}
	} else {
		analysis.TimeStatus = "unknown"
	}
	analysis.Snapshot, err = buildNodeQualitySnapshot(reader.File)
	if err != nil {
		return nodeQualityAnalysis{}, err
	}
	return analysis, nil
}

func readNodeQualityJSON(filename string, maximum int64) (any, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, errors.New("文件超过安全大小限制")
	}
	return decodeNodeQualityJSON(data)
}

func decodeNodeQualityJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("JSON 包含多个顶层值")
		}
		return nil, err
	}
	return value, nil
}

func writeNodeQualitySnapshot(filename string, snapshot nodeQualitySnapshot) error {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(snapshot); err != nil {
		return err
	}
	data := bytes.TrimSuffix(output.Bytes(), []byte("\n"))
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func readNodeQualityMember(member *zip.File) ([]byte, error) {
	if member.UncompressedSize64 > nqMaxMemberBytes {
		return nil, errors.New("NodeQuality ZIP 成员超过单文件限制")
	}
	reader, err := member.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, nqMaxMemberBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > nqMaxMemberBytes {
		return nil, errors.New("NodeQuality ZIP 成员实际内容超过单文件限制")
	}
	return data, nil
}

func decodeNodeQualityText(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	decoded, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), raw)
	if err == nil {
		return string(decoded)
	}
	return strings.ToValidUTF8(string(raw), "�")
}

func sanitizeNodeQualityANSI(value string) string {
	text := strings.ReplaceAll(value, "\r\n", "\n")
	// A lone CR only returns the xterm cursor to column zero. Treating it as a
	// newline adds dozens of blank rows to current NodeQuality logs.
	text = strings.ReplaceAll(text, "\r", "")
	text = nqPrivateUsePattern.ReplaceAllString(text, "")
	held := make([]string, 0)
	text = nqSGRPattern.ReplaceAllStringFunc(text, func(sequence string) string {
		held = append(held, sequence)
		return fmt.Sprintf("\ue000%d\ue001", len(held)-1)
	})
	text = nqOSCPattern.ReplaceAllString(text, "")
	text = nqANSIPattern.ReplaceAllString(text, "")
	text = nqControlPattern.ReplaceAllString(text, "")
	text = nqBidiPattern.ReplaceAllString(text, "")
	for index, sequence := range held {
		text = strings.ReplaceAll(text, fmt.Sprintf("\ue000%d\ue001", index), sequence)
	}
	return strings.TrimRight(text, "\n")
}

func sanitizeNodeQualityText(value string) string {
	return strings.TrimSpace(nqANSIPattern.ReplaceAllString(sanitizeNodeQualityANSI(value), ""))
}

func safeNodeQualitySnapshotMember(member *zip.File) bool {
	name := member.Name
	if member.FileInfo().IsDir() || member.UncompressedSize64 > nqMaxMemberBytes || name == "" {
		return false
	}
	if path.IsAbs(name) || strings.Contains(name, "\\") || utf8.RuneCountInString(name) > 160 {
		return false
	}
	for _, character := range name {
		if character < 32 || character == 127 {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".log", ".json", ".md", ".markdown":
		return true
	default:
		return false
	}
}

func normalizeNodeQualityPageTitle(value string) string {
	title := sanitizeNodeQualityText(value)
	title = strings.TrimSpace(nqLeadingTitlePattern.ReplaceAllString(title, ""))
	if title == "" {
		return "NodeQuality"
	}
	return truncateRunes(title, 32)
}

func nodeQualityPageIdentity(title, filename string) (string, string) {
	stem := strings.TrimSuffix(path.Base(filename), path.Ext(filename))
	text := strings.ToLower(title + " " + stem)
	switch {
	case strings.Contains(text, "全部"), strings.Contains(text, "header_info"),
		strings.Contains(text, "headerinfo"):
		return "all", "全部"
	case strings.Contains(text, "基本"), strings.Contains(text, "硬件"),
		strings.Contains(text, "hardware"), strings.Contains(text, "basic"):
		return "basic", "基本信息"
	case strings.Contains(text, "ip质量"), strings.Contains(text, "ip_quality"),
		strings.Contains(text, "ipquality"):
		return "ip-quality", "IP质量"
	case strings.Contains(text, "网络质量"), strings.Contains(text, "network_quality"),
		strings.Contains(text, "networkquality"), strings.Contains(text, "net_quality"):
		return "network-quality", "网络质量"
	case strings.Contains(text, "回程"), strings.Contains(text, "backtrace"),
		strings.Contains(text, "return"), strings.Contains(text, "route"):
		return "return-route", "回程路由"
	}
	slug := strings.Trim(nqSlugPattern.ReplaceAllString(strings.ToLower(stem), "-"), "-")
	if slug == "" {
		slug = "nodequality"
	}
	return truncateRunes(slug, 32), normalizeNodeQualityPageTitle(title)
}

func parseNodeQualityMarkdownPages(text, sourceName string) []nodeQualityPage {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	pages := make([]nodeQualityPage, 0)
	currentTitle := ""
	currentLines := make([]string, 0)
	finish := func() {
		if currentTitle == "" {
			return
		}
		body := strings.Trim(strings.Join(currentLines, "\n"), "\n")
		ansi := nqANSIFencePattern.FindStringSubmatch(body)
		image := nqImagePattern.FindString(body)
		pageID, title := nodeQualityPageIdentity(
			normalizeNodeQualityPageTitle(currentTitle), sourceName,
		)
		if len(ansi) > 1 {
			content := sanitizeNodeQualityANSI(ansi[1])
			if content != "" {
				pages = append(pages, nodeQualityPage{
					ID: pageID, Title: title, Format: "ansi", Content: content, Source: sourceName,
				})
			}
		} else if image != "" {
			pages = append(pages, nodeQualityPage{
				ID: pageID, Title: title, Format: "image", ImageURL: image, Source: sourceName,
			})
		}
	}
	for _, line := range strings.Split(text, "\n") {
		match := nqTabItemPattern.FindStringSubmatch(line)
		if len(match) > 1 {
			finish()
			currentTitle = match[1]
			currentLines = currentLines[:0]
		} else if currentTitle != "" && strings.TrimSpace(line) == ":::" {
			finish()
			currentTitle = ""
			currentLines = currentLines[:0]
		} else if currentTitle != "" {
			currentLines = append(currentLines, line)
		}
	}
	finish()
	return pages
}

func finalizeNodeQualitySnapshot(pages []nodeQualityPage, omitted int) nodeQualitySnapshot {
	snapshot := nodeQualitySnapshot{Version: 2, Pages: make([]nodeQualityPage, 0)}
	snapshot.Truncated = omitted > 0 || len(pages) > nqMaxSnapshotFiles
	if len(pages) > nqMaxSnapshotFiles {
		omitted += len(pages) - nqMaxSnapshotFiles
		pages = pages[:nqMaxSnapshotFiles]
	}
	usedIDs := map[string]bool{}
	totalText := 0
	for index, page := range pages {
		baseID := truncateRunes(page.ID, 32)
		pageID := baseID
		for suffixNumber := index + 1; usedIDs[pageID]; suffixNumber++ {
			suffix := fmt.Sprintf("-%d", suffixNumber)
			pageID = truncateRunes(baseID, 32-utf8.RuneCountInString(suffix)) + suffix
		}
		usedIDs[pageID] = true
		page.ID = pageID
		remaining := nqMaxSnapshotText - totalText
		if remaining > nqMaxSnapshotFileText {
			remaining = nqMaxSnapshotFileText
		}
		if remaining < 0 {
			remaining = 0
		}
		if len([]byte(page.Content)) > remaining {
			page.Content = truncateUTF8Bytes(page.Content, remaining)
			page.Content = strings.TrimRightFunc(
				nqPartialSGRPattern.ReplaceAllString(page.Content, ""), unicode.IsSpace,
			)
			page.Truncated = true
			snapshot.Truncated = true
		}
		totalText += len([]byte(page.Content))
		if page.Format == "ansi" && page.Content == "" {
			omitted++
			snapshot.Truncated = true
			continue
		}
		page.Source = truncateRunes(page.Source, 160)
		snapshot.Pages = append(snapshot.Pages, page)
	}
	snapshot.OmittedFiles = omitted
	return snapshot
}

func buildNodeQualitySnapshot(members []*zip.File) (nodeQualitySnapshot, error) {
	safeMembers := make([]*zip.File, 0)
	for _, member := range members {
		if safeNodeQualitySnapshotMember(member) {
			safeMembers = append(safeMembers, member)
		}
	}
	markdown := make([]*zip.File, 0)
	nativePages := map[string]bool{}
	for _, member := range safeMembers {
		extension := strings.ToLower(path.Ext(member.Name))
		if extension == ".md" || extension == ".markdown" {
			markdown = append(markdown, member)
		}
		switch strings.ToLower(path.Base(member.Name)) {
		case "header_info.log":
			nativePages["all"] = true
		case "basic_info.log", "hardware_quality.log":
			nativePages["basic"] = true
		case "ip_quality.log":
			nativePages["ip-quality"] = true
		case "net_quality.log":
			nativePages["network-quality"] = true
		case "backroute_trace.log":
			nativePages["return-route"] = true
		}
	}
	hasNativeLogs := len(nativePages) == 5
	sort.SliceStable(markdown, func(left, right int) bool {
		leftReport := strings.Contains(strings.ToLower(markdown[left].Name), "report")
		rightReport := strings.Contains(strings.ToLower(markdown[right].Name), "report")
		if leftReport != rightReport {
			return leftReport
		}
		return strings.ToLower(markdown[left].Name) < strings.ToLower(markdown[right].Name)
	})
	if !hasNativeLogs {
		for _, member := range markdown {
			raw, err := readNodeQualityMember(member)
			if err != nil {
				return nodeQualitySnapshot{}, err
			}
			pages := parseNodeQualityMarkdownPages(decodeNodeQualityText(raw), member.Name)
			if len(pages) > 0 {
				return finalizeNodeQualitySnapshot(pages, 0), nil
			}
		}
	}

	pageMap := map[string]nodeQualityPage{}
	for _, member := range safeMembers {
		if !strings.HasSuffix(strings.ToLower(member.Name), ".log") {
			continue
		}
		baseName := strings.ToLower(path.Base(member.Name))
		if hasNativeLogs {
			switch baseName {
			case "header_info.log", "basic_info.log", "hardware_quality.log",
				"ip_quality.log", "net_quality.log", "backroute_trace.log":
			default:
				continue
			}
		}
		raw, err := readNodeQualityMember(member)
		if err != nil {
			return nodeQualitySnapshot{}, err
		}
		content := sanitizeNodeQualityANSI(decodeNodeQualityText(raw))
		if content == "" {
			continue
		}
		stem := strings.TrimSuffix(path.Base(member.Name), path.Ext(member.Name))
		pageID, title := nodeQualityPageIdentity(stem, member.Name)
		page := nodeQualityPage{
			ID: pageID, Title: title, Format: "ansi", Content: content, Source: member.Name,
		}
		if existing, exists := pageMap[pageID]; exists && pageID == "basic" {
			existingIsHardware := strings.Contains(strings.ToLower(existing.Source), "hardware_quality.log")
			incomingIsHardware := baseName == "hardware_quality.log"
			if incomingIsHardware || existingIsHardware {
				if incomingIsHardware {
					pageMap[pageID] = page
				}
				continue
			}
		}
		if existing, exists := pageMap[pageID]; exists {
			if existing.Format == "ansi" {
				existing.Content += "\n\n" + content
				existing.Source += "," + member.Name
				pageMap[pageID] = existing
			}
			continue
		}
		pageMap[pageID] = page
	}
	allParts := make([]string, 0, 5)
	allSources := make([]string, 0, 5)
	if header, exists := pageMap["all"]; exists && header.Format == "ansi" {
		allParts = append(allParts, header.Content)
		allSources = append(allSources, header.Source)
	}
	for _, pageID := range []string{"basic", "ip-quality", "network-quality", "return-route"} {
		if page, exists := pageMap[pageID]; exists && page.Format == "ansi" {
			allParts = append(allParts, page.Content)
			allSources = append(allSources, page.Source)
		}
	}
	if len(allParts) > 0 {
		pageMap["all"] = nodeQualityPage{
			ID:      "all",
			Title:   "全部",
			Format:  "ansi",
			Content: strings.Join(allParts, "\n\n"),
			Source:  strings.Join(allSources, ","),
		}
	}
	priority := map[string]int{
		"all": 0, "basic": 1, "ip-quality": 2, "network-quality": 3, "return-route": 4,
	}
	pages := make([]nodeQualityPage, 0, len(pageMap))
	for _, page := range pageMap {
		pages = append(pages, page)
	}
	sort.SliceStable(pages, func(left, right int) bool {
		leftPriority, exists := priority[pages[left].ID]
		if !exists {
			leftPriority = 100
		}
		rightPriority, exists := priority[pages[right].ID]
		if !exists {
			rightPriority = 100
		}
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return pages[left].Title < pages[right].Title
	})
	return finalizeNodeQualitySnapshot(pages, 0), nil
}

func findNodeQualityReportIPs(members []*zip.File) ([]string, error) {
	output := make([]string, 0)
	seen := map[string]bool{}
	appendCandidate := func(candidate string) {
		if candidate != "" && !seen[candidate] {
			seen[candidate] = true
			output = append(output, candidate)
		}
	}
	for _, member := range members {
		base := path.Base(member.Name)
		if (base != "ip_quality.json" && base != "ip_quality.log") ||
			member.UncompressedSize64 > nqMaxMemberBytes {
			continue
		}
		raw, err := readNodeQualityMember(member)
		if err != nil {
			return nil, err
		}
		text := decodeNodeQualityText(raw)
		if strings.HasSuffix(base, ".json") {
			for _, match := range nqJSONIPPattern.FindAllStringSubmatch(text, -1) {
				if len(match) > 1 {
					appendCandidate(match[1])
				}
			}
			continue
		}
		header := truncateRunes(text, 4096)
		header = nqANSIPattern.ReplaceAllString(header, "")
		if match := nqIdentityPattern.FindStringSubmatch(header); len(match) > 1 {
			appendCandidate(match[1])
			continue
		}
		if match := nqIPv4Pattern.FindStringSubmatch(header); len(match) > 1 {
			appendCandidate(match[1])
		}
	}
	return output, nil
}

func nodeQualityIdentityMatches(currentIP, reportIP, currentASN, reportASN string) (bool, string) {
	if !strings.Contains(reportIP, "*") {
		current, currentErr := netip.ParseAddr(currentIP)
		report, reportErr := netip.ParseAddr(reportIP)
		if currentErr != nil || reportErr != nil {
			return false, "invalid_report_ip"
		}
		return current.Unmap() == report.Unmap(), "full_ip"
	}
	current, err := netip.ParseAddr(currentIP)
	if err != nil || !current.Is4() {
		return false, "masked_ipv4_without_current_ipv4"
	}
	parts := strings.Split(reportIP, ".")
	if len(parts) != 4 {
		return false, "invalid_masked_ip"
	}
	if parts[0] == "*" || parts[1] == "*" {
		return false, "masked_ip_too_broad"
	}
	currentParts := strings.Split(current.String(), ".")
	concrete := 0
	wildcardSeen := false
	for index, expected := range parts {
		if expected == "*" {
			wildcardSeen = true
			continue
		}
		if wildcardSeen {
			return false, "invalid_masked_ip"
		}
		value, err := strconv.Atoi(expected)
		actual, actualErr := strconv.Atoi(currentParts[index])
		if err != nil || actualErr != nil || value < 0 || value > 255 || value != actual {
			return false, "masked_ip_prefix_differs"
		}
		concrete++
	}
	if concrete < 2 {
		return false, "masked_ip_too_broad"
	}
	if currentASN == "" || reportASN == "" || currentASN != reportASN {
		return false, "masked_ip_asn_differs"
	}
	return true, "masked_ip_and_asn"
}

func findNodeQualityReportTime(members []*zip.File, apiResponse any) (nodeQualityTimeCandidate, error) {
	best := nodeQualityTimeCandidate{}
	consider := func(value any, source string) {
		parsed, ok := parseNodeQualityReportTime(value)
		if ok && (best.Time.IsZero() || parsed.After(best.Time)) {
			best = nodeQualityTimeCandidate{Time: parsed, Source: source}
		}
	}
	for _, member := range members {
		extension := strings.ToLower(path.Ext(member.Name))
		if (extension != ".log" && extension != ".md" && extension != ".markdown") ||
			member.UncompressedSize64 > nqMaxMemberBytes {
			continue
		}
		raw, err := readNodeQualityMember(member)
		if err != nil {
			return nodeQualityTimeCandidate{}, err
		}
		text := nqANSIPattern.ReplaceAllString(decodeNodeQualityText(raw), "")
		for _, match := range nqReportTimePattern.FindAllString(text, -1) {
			consider(match, path.Base(member.Name))
		}
	}
	for _, member := range members {
		if !strings.HasSuffix(strings.ToLower(member.Name), ".json") ||
			member.UncompressedSize64 > nqMaxMemberBytes {
			continue
		}
		raw, err := readNodeQualityMember(member)
		if err != nil {
			return nodeQualityTimeCandidate{}, err
		}
		document, err := decodeNodeQualityJSON([]byte(decodeNodeQualityText(raw)))
		if err != nil {
			continue
		}
		collectNodeQualityJSONTimes(document, "", 0, func(value any) {
			consider(value, path.Base(member.Name))
		})
	}
	collectNodeQualityJSONTimes(apiResponse, "", 0, func(value any) {
		consider(value, "NodeQuality API")
	})
	return best, nil
}

func collectNodeQualityJSONTimes(value any, parent string, depth int, visit func(any)) {
	if depth > 128 {
		return
	}
	switch item := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(item))
		for key := range item {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := item[key]
			normalized := normalizeNodeQualityJSONKey(key)
			if nqTimeKeys[normalized] || (normalized == "time" &&
				(parent == "head" || parent == "header" || parent == "meta" || parent == "metadata")) {
				visit(child)
			}
			collectNodeQualityJSONTimes(child, normalized, depth+1, visit)
		}
	case []any:
		for index, child := range item {
			collectNodeQualityJSONTimes(child, strconv.Itoa(index), depth+1, visit)
		}
	}
}

func parseNodeQualityReportTime(value any) (time.Time, bool) {
	switch item := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(item.String(), 64)
		if err != nil {
			return time.Time{}, false
		}
		return parseNodeQualityEpoch(parsed)
	case float64:
		return parseNodeQualityEpoch(item)
	case float32:
		return parseNodeQualityEpoch(float64(item))
	case int:
		return parseNodeQualityEpoch(float64(item))
	case int64:
		return parseNodeQualityEpoch(float64(item))
	case string:
		clean := strings.TrimSpace(nqANSIPattern.ReplaceAllString(item, ""))
		if nqNumericTimePattern.MatchString(clean) {
			number, err := strconv.ParseFloat(clean, 64)
			if err == nil {
				return parseNodeQualityEpoch(number)
			}
		}
		match := nqReportTimePattern.FindStringSubmatch(clean)
		if len(match) < 2 {
			match = nqISOTimePattern.FindStringSubmatch(clean)
		}
		if len(match) < 2 {
			return time.Time{}, false
		}
		zone := ""
		if len(match) > 2 {
			zone = match[2]
		}
		return parseNodeQualityDate(match[1], zone)
	default:
		return time.Time{}, false
	}
}

func parseNodeQualityEpoch(value float64) (time.Time, bool) {
	if value > 10_000_000_000 {
		value /= 1000
	}
	seconds := int64(value)
	parsed := time.Unix(seconds, 0).UTC()
	if parsed.Year() < 2000 || parsed.Year() > 2100 {
		return time.Time{}, false
	}
	return parsed, true
}

func parseNodeQualityDate(dateText, zoneText string) (time.Time, bool) {
	match := nqDatePattern.FindStringSubmatch(dateText)
	if len(match) != 8 {
		return time.Time{}, false
	}
	values := make([]int, 6)
	for index := range values {
		if match[index+1] == "" {
			continue
		}
		parsed, err := strconv.Atoi(match[index+1])
		if err != nil {
			return time.Time{}, false
		}
		values[index] = parsed
	}
	nanoseconds := 0
	if fraction := match[7]; fraction != "" {
		if len(fraction) > 9 {
			fraction = fraction[:9]
		}
		fraction += strings.Repeat("0", 9-len(fraction))
		nanoseconds, _ = strconv.Atoi(fraction)
	}
	location, ok := nodeQualityTimeZone(zoneText)
	if !ok {
		return time.Time{}, false
	}
	parsed := time.Date(
		values[0], time.Month(values[1]), values[2], values[3], values[4], values[5], nanoseconds, location,
	)
	if parsed.Year() != values[0] || int(parsed.Month()) != values[1] || parsed.Day() != values[2] ||
		parsed.Hour() != values[3] || parsed.Minute() != values[4] || parsed.Second() != values[5] {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func nodeQualityTimeZone(value string) (*time.Location, bool) {
	zone := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
	if zone == "Z" || zone == "UTC" || zone == "GMT" {
		return time.UTC, true
	}
	if zone == "" || zone == "CST" || zone == "北京时间" {
		return time.FixedZone("CST", 8*60*60), true
	}
	if strings.HasPrefix(zone, "UTC+") || strings.HasPrefix(zone, "UTC-") ||
		strings.HasPrefix(zone, "GMT+") || strings.HasPrefix(zone, "GMT-") {
		zone = zone[3:]
	}
	if len(zone) < 2 || (zone[0] != '+' && zone[0] != '-') {
		return nil, false
	}
	sign := 1
	if zone[0] == '-' {
		sign = -1
	}
	digits := strings.ReplaceAll(zone[1:], ":", "")
	if len(digits) == 1 {
		digits = "0" + digits
	}
	if len(digits) == 2 {
		digits += "00"
	}
	if len(digits) != 4 {
		return nil, false
	}
	hours, hourErr := strconv.Atoi(digits[:2])
	minutes, minuteErr := strconv.Atoi(digits[2:])
	if hourErr != nil || minuteErr != nil || hours > 23 || minutes > 59 {
		return nil, false
	}
	return time.FixedZone("", sign*(hours*60+minutes)*60), true
}

func cleanASN(value any) string {
	match := nqDigitsPattern.FindString(stringValue(value))
	return match
}

func stringValue(value any) string {
	switch item := value.(type) {
	case nil:
		return ""
	case string:
		return item
	case json.Number:
		return item.String()
	default:
		return fmt.Sprint(item)
	}
}

func normalizeNodeQualityJSONKey(value string) string {
	var output strings.Builder
	for _, character := range strings.ToLower(value) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			output.WriteRune(character)
		}
	}
	return output.String()
}

func truncateRunes(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}

func truncateUTF8Bytes(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	data := []byte(value)
	if len(data) <= maximum {
		return value
	}
	data = data[:maximum]
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return string(data)
}
