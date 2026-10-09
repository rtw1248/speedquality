package main

import (
	"bytes"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

func TestReportUsesANSIColorsAndDisplayWidthAlignment(t *testing.T) {
	report := Report{
		Region: Region{Code: "hb", Name: "湖北"}, Family: "v4", TargetMbps: 200,
		Results: []MeasurementResult{
			{
				Label: "湖北电信", Status: "ok", Latency: 8.25,
				Single: &ModeResult{UploadMbps: 150.5, DownloadMbps: 199.2},
			},
			{Label: "湖北联通", Status: "failed", Error: "节点不可用"},
			{Label: "湖北移动", Status: "failed", Error: "没有可连接的候选节点"},
		},
	}
	var output bytes.Buffer
	printReport(&output, report)
	text := output.String()
	for _, sequence := range []string{"\x1b[36m", "\x1b[32m", "\x1b[33m", "\x1b[31m"} {
		if !strings.Contains(text, sequence) {
			t.Fatalf("report lacks ANSI sequence %q: %q", sequence, text)
		}
	}
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(text, "")
	heading := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "IPv4") && strings.Contains(line, "单线程下载") {
			heading = line
			break
		}
	}
	if strings.Count(heading, cyan) != 4 || strings.Contains(heading, yellow) {
		t.Fatalf("report headings do not share the IPv4 color: %q", heading)
	}
	for _, expected := range []string{
		"        IPv4        延迟          单线程上传          单线程下载",
		"    湖北电信         8ms          150.50Mbps           200Mbps ✓",
		"[失败] 湖北联通：节点不可用",
		"    湖北移动           -                   -                   -",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("plain report lacks %q:\n%s", expected, plain)
		}
	}
	if strings.Contains(plain, "[失败] 湖北移动") || strings.Contains(plain, "没有可连接的候选节点") {
		t.Fatalf("unavailable node should remain a compact table row:\n%s", plain)
	}
	if displayWidth("湖北电信") != 8 || displayWidth(padDisplay("湖北电信", 12, "right")) != 12 {
		t.Fatal("Chinese display width padding is incorrect")
	}
}

func TestPrintReportKeepsPartialMeasurementsVisible(t *testing.T) {
	report := Report{
		Region: Region{Name: "湖北"}, Family: "v4", TargetMbps: 200,
		Results: []MeasurementResult{{
			Label: "湖北电信", Status: "failed", Latency: 20.5,
			Error:  "上传未产生有效数据",
			Single: &ModeResult{DownloadMbps: 123.45, DownloadBytes: 1_000_000},
		}},
	}
	var output bytes.Buffer
	printReport(&output, report)
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(output.String(), "")
	if !strings.Contains(plain, "21ms") || !strings.Contains(plain, "123.45Mbps") ||
		!strings.Contains(plain, "上传未产生有效数据") {
		t.Fatalf("partial failure details were hidden: %q", plain)
	}
}

func TestDiagnosticErrorDoesNotExposeNodeURL(t *testing.T) {
	err := &url.Error{
		Op:  "Get",
		URL: "http://192.0.2.10/activate?token=secret-value",
		Err: errors.Join(syscall.ECONNREFUSED, errors.New("private endpoint detail")),
	}
	for _, value := range []string{diagnosticError(err), publicNetworkError(err)} {
		if strings.Contains(value, "192.0.2.10") || strings.Contains(value, "secret-value") {
			t.Fatalf("diagnostic error leaked endpoint data: %q", value)
		}
	}
}
