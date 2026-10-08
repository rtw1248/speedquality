package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func nodeQualityArchive(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for name, content := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func nodeQualityArchiveFiles(t *testing.T, archive []byte) []*zip.File {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	return reader.File
}

func writeNodeQualityFixture(
	t *testing.T,
	currentIP string,
	currentASN any,
	reportASN string,
	entries map[string][]byte,
) (string, string) {
	t.Helper()
	directory := t.TempDir()
	ipInfoPath := filepath.Join(directory, "ipinfo.json")
	recordPath := filepath.Join(directory, "record.json")
	current, err := json.Marshal(map[string]any{"ip": currentIP, "asn": currentASN})
	if err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(map[string]any{
		"success": true,
		"data": map[string]any{
			"asn":    reportASN,
			"result": base64.StdEncoding.EncodeToString(nodeQualityArchive(t, entries)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ipInfoPath, current, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, record, 0o600); err != nil {
		t.Fatal(err)
	}
	return ipInfoPath, recordPath
}

func gb18030Text(t *testing.T, value string) []byte {
	t.Helper()
	encoded, _, err := transform.Bytes(simplifiedchinese.GB18030.NewEncoder(), []byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestNodeQualityIdentityMatching(t *testing.T) {
	tests := []struct {
		name                          string
		currentIP, reportIP           string
		currentASN, reportASN, reason string
		matched                       bool
	}{
		{"full IPv4", "45.78.1.2", "45.78.1.2", "25820", "25820", "full_ip", true},
		{"full IPv6", "2001:db8::1", "2001:db8::1", "25820", "9999", "full_ip", true},
		{"masked IPv4 and ASN", "45.78.1.2", "45.78.*.*", "25820", "25820", "masked_ip_and_asn", true},
		{"masked ASN differs", "45.78.1.2", "45.78.*.*", "25820", "9999", "masked_ip_asn_differs", false},
		{"mask is too broad", "45.78.1.2", "45.*.*.*", "25820", "25820", "masked_ip_too_broad", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matched, reason := nodeQualityIdentityMatches(
				test.currentIP, test.reportIP, test.currentASN, test.reportASN,
			)
			if matched != test.matched || reason != test.reason {
				t.Fatalf("match = %v, reason = %q", matched, reason)
			}
		})
	}
}

func TestNodeQualityUsesLatestReportTime(t *testing.T) {
	archive := nodeQualityArchive(t, map[string][]byte{
		"header_info.log": []byte("报告时间：2026-09-29 10:00:00 CST\n"),
		"details.json":    []byte(`{"meta":{"testTime":"2026-09-29T11:00:00+08:00"}}`),
	})
	latest, err := findNodeQualityReportTime(
		nodeQualityArchiveFiles(t, archive),
		map[string]any{"metadata": map[string]any{"generatedAt": "2026-09-29 12:00:00 UTC+8"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 29, 4, 0, 0, 0, time.UTC)
	if !latest.Time.Equal(want) || latest.Source != "NodeQuality API" {
		t.Fatalf("latest = %v from %q, want %v from API", latest.Time, latest.Source, want)
	}
}

func TestNodeQualitySixtyMinuteBoundary(t *testing.T) {
	ipInfo, record := writeNodeQualityFixture(
		t, "45.78.1.2", 25820, "25820",
		map[string][]byte{
			"ip_quality.log":  []byte("IP质量体检报告：45.78.*.*\n"),
			"header_info.log": []byte("报告时间：2026-09-29 12:00:00 CST\n"),
		},
	)
	reportTime := time.Date(2026, 9, 29, 4, 0, 0, 0, time.UTC)
	closeAnalysis, err := verifyNodeQuality(ipInfo, record, reportTime.Add(60*time.Minute).Unix(), 60)
	if err != nil {
		t.Fatal(err)
	}
	if closeAnalysis.Status != "match" || closeAnalysis.TimeStatus != "close" {
		t.Fatalf("60-minute boundary = %q/%q", closeAnalysis.Status, closeAnalysis.TimeStatus)
	}
	staleAnalysis, err := verifyNodeQuality(ipInfo, record, reportTime.Add(60*time.Minute+time.Second).Unix(), 60)
	if err != nil {
		t.Fatal(err)
	}
	if staleAnalysis.TimeStatus != "stale" {
		t.Fatalf("60 minutes and one second = %q", staleAnalysis.TimeStatus)
	}
}

func TestNodeQualityReadsGB18030ReportTime(t *testing.T) {
	archive := nodeQualityArchive(t, map[string][]byte{
		"nodequality.md": gb18030Text(t, "报告时间：2026-09-29 12:34:56 CST\n"),
	})
	latest, err := findNodeQualityReportTime(nodeQualityArchiveFiles(t, archive), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 29, 4, 34, 56, 0, time.UTC)
	if !latest.Time.Equal(want) {
		t.Fatalf("GB18030 time = %v, want %v", latest.Time, want)
	}
}

func TestNodeQualitySnapshotPreservesPagesAndCleansControls(t *testing.T) {
	markdown := ":::: tabs\r\n" +
		"::: tab-item ??基本信息\r\n\r\n```ansi\r\n" +
		"  保留  对齐空格\r\n\x1b[31m红色标题\x1b[0m\r\n" +
		"\x1b]0;unsafe\x07控制符后文本\x01\r\n```\r\n\r\n:::\r\n" +
		"::: tab-item ??IP质量\r\n\r\n```ansi\r\nIP质量体检报告：45.78.*.*\r\n```\r\n\r\n:::\r\n" +
		"::: tab-item ??网络质量\r\nhttps://images.example/network.webp\r\n\r\n:::\r\n" +
		"::: tab-item ??回程路由\r\nhttps://images.example/route.png\r\n\r\n:::\r\n::::\r\n"
	archive := nodeQualityArchive(t, map[string][]byte{
		"nodequality.md": gb18030Text(t, markdown),
		"../escape.log":  []byte("must not appear"),
	})
	snapshot, err := buildNodeQualitySnapshot(nodeQualityArchiveFiles(t, archive))
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"basic", "ip-quality", "network-quality", "return-route"}
	wantFormats := []string{"ansi", "ansi", "image", "image"}
	if len(snapshot.Pages) != len(wantIDs) {
		t.Fatalf("pages = %#v", snapshot.Pages)
	}
	var content strings.Builder
	for index, page := range snapshot.Pages {
		if page.ID != wantIDs[index] || page.Format != wantFormats[index] {
			t.Fatalf("page %d = %q/%q", index, page.ID, page.Format)
		}
		content.WriteString(page.Content)
	}
	text := content.String()
	if !strings.Contains(text, "  保留  对齐空格") || !strings.Contains(text, "\x1b[31m红色标题\x1b[0m") {
		t.Fatalf("expected layout or SGR sequence missing: %q", text)
	}
	if strings.Contains(text, "\x1b]") || strings.ContainsRune(text, '\x01') || strings.Contains(text, "must not appear") {
		t.Fatalf("unsafe snapshot content remains: %q", text)
	}
	if snapshot.Pages[2].ImageURL != "https://images.example/network.webp" ||
		snapshot.Pages[3].ImageURL != "https://images.example/route.png" {
		t.Fatalf("image pages = %#v", snapshot.Pages[2:])
	}
}

func TestNodeQualitySnapshotMatchesCurrentFiveTabLayout(t *testing.T) {
	networkBody := "NETWORK-BEGIN\n" + strings.Repeat("network row\n", 2300) +
		"https://report.example/network.svg\nNETWORK-END"
	archive := nodeQualityArchive(t, map[string][]byte{
		"header_info.log":      []byte("HEADER\n"),
		"basic_info.log":       []byte("OLD BASIC\n"),
		"hardware_quality.log": []byte("\x1b[H\x1b[2J\x1b[3J\r\rHARDWARE\n"),
		"ip_quality.log":       []byte("\x1b[H\x1b[2J\x1b[3J\r\rIP QUALITY\n"),
		"net_quality.log":      []byte(networkBody),
		"backroute_trace.log":  []byte("RETURN ROUTE\n"),
	})
	snapshot, err := buildNodeQualitySnapshot(nodeQualityArchiveFiles(t, archive))
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"all", "basic", "ip-quality", "network-quality", "return-route"}
	if len(snapshot.Pages) != len(wantIDs) {
		t.Fatalf("pages = %#v", snapshot.Pages)
	}
	for index, page := range snapshot.Pages {
		if page.ID != wantIDs[index] {
			t.Fatalf("page %d ID = %q, want %q", index, page.ID, wantIDs[index])
		}
		if page.Truncated {
			t.Fatalf("page %q unexpectedly truncated", page.ID)
		}
	}
	all := snapshot.Pages[0].Content
	positions := []int{
		strings.Index(all, "HEADER"),
		strings.Index(all, "HARDWARE"),
		strings.Index(all, "IP QUALITY"),
		strings.Index(all, "NETWORK-BEGIN"),
		strings.Index(all, "RETURN ROUTE"),
	}
	for index, position := range positions {
		if position < 0 || index > 0 && position <= positions[index-1] {
			t.Fatalf("unexpected all-page order: %v", positions)
		}
	}
	if strings.Contains(all, "OLD BASIC") || strings.HasPrefix(snapshot.Pages[1].Content, "\n") {
		t.Fatalf("hardware override or CR normalization failed: %q", snapshot.Pages[1].Content)
	}
	if !strings.Contains(snapshot.Pages[3].Content, "NETWORK-END") {
		t.Fatal("network page was cut at the former 24 KiB limit")
	}
	if snapshot.Pages[3].Format != "ansi" {
		t.Fatal("an export image URL must not replace the native terminal log")
	}
}

func TestNodeQualitySnapshotSkipsTraversalMember(t *testing.T) {
	archive := nodeQualityArchive(t, map[string][]byte{
		"header_info.log": []byte("safe content"),
		"../escape.log":   []byte("traversal content"),
	})
	snapshot, err := buildNodeQualitySnapshot(nodeQualityArchiveFiles(t, archive))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "traversal content") || !strings.Contains(string(encoded), "safe content") {
		t.Fatalf("unexpected snapshot: %s", encoded)
	}
}

func TestNodeQualityRejectsTooManyArchiveMembers(t *testing.T) {
	entries := map[string][]byte{
		"ip_quality.log": []byte("IP质量体检报告：45.78.*.*\n"),
	}
	for index := 0; index < nqMaxMembers; index++ {
		entries["checks/member-"+time.Unix(int64(index), 0).Format("150405.000000000")+".log"] = []byte("fixture")
	}
	ipInfo, record := writeNodeQualityFixture(t, "45.78.1.2", 25820, "25820", entries)
	if _, err := verifyNodeQuality(ipInfo, record, time.Now().Unix(), 15); err == nil ||
		!strings.Contains(err.Error(), "ZIP 成员过多") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNodeQualitySnapshotIsAlwaysPrivate(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(filename, []byte("old"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filename, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := writeNodeQualitySnapshot(filename, nodeQualitySnapshot{Version: 2}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if permission := info.Mode().Perm(); permission != 0o600 {
		t.Fatalf("snapshot mode = %o, want 600", permission)
	}
}
