package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
)

var version = "dev"

const (
	reset    = "\x1b[0m"
	boldCyan = "\x1b[1;36m"
	cyan     = "\x1b[36m"
	green    = "\x1b[32m"
	yellow   = "\x1b[33m"
	red      = "\x1b[31m"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "nq-verify" {
		if err := runNodeQualityCommand(os.Args[2:], os.Stdout, os.Stderr); err != nil {
			if err == flag.ErrHelp {
				return
			}
			fmt.Fprintf(os.Stderr, "[X] NodeQuality 校验失败: %v\n", err)
			os.Exit(2)
		}
		return
	}

	leasePath := flag.String("lease", "", "节点租约 JSON 文件")
	outputPath := flag.String("output", "", "将结构化结果写入 NDJSON 文件")
	appendOutput := flag.Bool("append", false, "追加写入结果文件")
	diagnosticPath := flag.String("diagnostic-log", "", "将诊断事件写入 JSONL 文件")
	jsonOutput := flag.Bool("json", false, "在标准输出打印 JSON")
	showVersion := flag.Bool("version", false, "显示版本")
	flag.Parse()
	if *showVersion {
		fmt.Printf("sqprobe %s\n", version)
		return
	}
	if *leasePath == "" {
		fmt.Fprintln(os.Stderr, "[X] --lease 是必填参数")
		os.Exit(2)
	}
	lease, err := loadLease(*leasePath, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "[X] 节点租约无效: %v\n", err)
		os.Exit(2)
	}
	ctx, diagnosticFile, err := withDiagnosticLog(context.Background(), *diagnosticPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[X] 无法创建诊断日志: %v\n", err)
		os.Exit(1)
	}
	if diagnosticFile != nil {
		defer diagnosticFile.Close()
	}
	progress := newProgressTracker(
		os.Stderr,
		len(lease.Targets)*3,
		fmt.Sprintf("%s %s", lease.Region.Name, displayFamily(lease.Family)),
		terminalProgressEnabled(),
	)
	report := runLease(ctx, lease, progress)
	progress.Finish("测速完成")
	encoded, err := json.Marshal(report)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[X] 无法生成结果: %v\n", err)
		os.Exit(1)
	}
	if *outputPath != "" {
		if err := writeResult(*outputPath, encoded, *appendOutput); err != nil {
			fmt.Fprintf(os.Stderr, "[X] 无法写入结果: %v\n", err)
			os.Exit(1)
		}
	}
	if *jsonOutput {
		fmt.Println(string(encoded))
	} else {
		printReport(os.Stdout, report)
	}
	for _, result := range report.Results {
		if result.Status == "ok" {
			return
		}
	}
	os.Exit(1)
}

func writeResult(path string, encoded []byte, appendOutput bool) error {
	flags := os.O_WRONLY | os.O_CREATE
	if appendOutput {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(encoded); err != nil {
		return err
	}
	_, err = file.Write([]byte("\n"))
	return err
}

func printReport(writer io.Writer, report Report) {
	family := displayFamily(report.Family)
	fmt.Fprintf(
		writer,
		"\n%sSpeedQuality 单线程测速%s  %s  %d Mbps 档位\n\n",
		boldCyan, reset, report.Region.Name, report.TargetMbps,
	)
	writeColumns(writer, []tableColumn{
		{family, 12, "right", cyan},
		{"延迟", 10, "right", yellow},
		{"单线程上传", 18, "right", yellow},
		{"单线程下载", 18, "right", yellow},
	}, reset)
	for _, result := range report.Results {
		if result.Status != "ok" {
			latency := "-"
			download, upload := "失败", "失败"
			unavailable := result.Error == "没有可连接的候选节点"
			if unavailable {
				download, upload = "-", "-"
			}
			if result.Latency > 0 {
				latency = fmt.Sprintf("%.2fms", result.Latency)
			}
			if result.Single != nil {
				if result.Single.DownloadMbps > 0 {
					download = displaySpeed(result.Single.DownloadMbps, report.TargetMbps)
				}
				if result.Single.UploadMbps > 0 {
					upload = displaySpeed(result.Single.UploadMbps, report.TargetMbps)
				}
			}
			writeColumns(writer, []tableColumn{
				{result.Label, 12, "right", cyan},
				{latency, 10, "right", red},
				{upload, 18, "right", red},
				{download, 18, "right", red},
			}, reset)
			if !unavailable {
				fmt.Fprintf(writer, "  %s[失败] %s：%s%s\n", red, result.Label, result.Error, reset)
			}
			continue
		}
		download, upload := "-", "-"
		downloadColor, uploadColor := red, red
		if result.Single != nil {
			download = displaySpeed(result.Single.DownloadMbps, report.TargetMbps)
			upload = displaySpeed(result.Single.UploadMbps, report.TargetMbps)
			downloadColor = speedColor(result.Single.DownloadMbps, report.TargetMbps)
			uploadColor = speedColor(result.Single.UploadMbps, report.TargetMbps)
		}
		writeColumns(writer, []tableColumn{
			{result.Label, 12, "right", cyan},
			{fmt.Sprintf("%.2fms", result.Latency), 10, "right", latencyColor(result.Latency)},
			{upload, 18, "right", uploadColor},
			{download, 18, "right", downloadColor},
		}, reset)
	}
}

func displayFamily(value string) string {
	switch strings.ToLower(value) {
	case "v4":
		return "IPv4"
	case "v6":
		return "IPv6"
	default:
		return strings.ToUpper(value)
	}
}

type tableColumn struct {
	text  string
	width int
	align string
	color string
}

func writeColumns(writer io.Writer, columns []tableColumn, reset string) {
	for index, column := range columns {
		if index > 0 {
			_, _ = io.WriteString(writer, "  ")
		}
		padded := padDisplay(column.text, column.width, column.align)
		_, _ = fmt.Fprintf(writer, "%s%s%s", column.color, padded, reset)
	}
	_, _ = io.WriteString(writer, "\n")
}

func displayWidth(value string) int {
	width := 0
	for _, character := range value {
		switch {
		case character == 0:
		case character == '✓':
			width++
		case unicode.Is(unicode.Mn, character), unicode.Is(unicode.Me, character), unicode.Is(unicode.Cf, character):
		case character >= 0x1100:
			width += 2
		default:
			width++
		}
	}
	return width
}

func padDisplay(value string, width int, align string) string {
	padding := width - displayWidth(value)
	if padding < 0 {
		padding = 0
	}
	if align == "right" {
		return strings.Repeat(" ", padding) + value
	}
	return value + strings.Repeat(" ", padding)
}

func reachedTarget(value float64, targetMbps int) bool {
	return targetMbps > 0 && value >= float64(targetMbps)*0.98
}

func displaySpeed(value float64, targetMbps int) string {
	if reachedTarget(value, targetMbps) {
		return fmt.Sprintf("%dMbps ✓", targetMbps)
	}
	return fmt.Sprintf("%.2fMbps", value)
}

func speedColor(value float64, targetMbps int) string {
	if targetMbps <= 0 || value < float64(targetMbps)*0.3 {
		return red
	}
	if value < float64(targetMbps)*0.8 {
		return yellow
	}
	return green
}

func latencyColor(value float64) string {
	if value <= 100 {
		return green
	}
	if value <= 200 {
		return yellow
	}
	return red
}
