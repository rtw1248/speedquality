package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const queueWaitBudget = 30 * time.Second

type scheduledTask struct {
	Region  Region
	Family  string
	Carrier string
	Result  MeasurementResult
	Pending bool
}

func (t *scheduledTask) label() string {
	return t.Region.Name + displayCarrier(MeasurementResult{Carrier: t.Carrier}) + " " + displayFamily(t.Family)
}

type taskReply struct {
	Lease   Lease
	Waiting bool
	Reason  string
	Retry   bool
}

type taskControl interface {
	Call(context.Context, *scheduledTask, string, bool, *Lease, *MeasurementResult) (taskReply, error)
}

type httpTaskControl struct {
	base    string
	tokens  map[string]string
	clients map[string]*http.Client
	now     func() time.Time
}

func newHTTPTaskControl(base string, tokens map[string]string, now func() time.Time) (*httpTaskControl, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("调度服务地址无效")
	}
	control := &httpTaskControl{base: strings.TrimRight(base, "/"), tokens: tokens, now: now, clients: map[string]*http.Client{}}
	for _, family := range []string{"v4", "v6"} {
		network := "tcp4"
		if family == "v6" {
			network = "tcp6"
		}
		dialer := &net.Dialer{Timeout: 8 * time.Second}
		transport := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 8 * time.Second,
			DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, address)
			},
		}
		control.clients[family] = &http.Client{Transport: transport, Timeout: 30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return control, nil
}

func (c *httpTaskControl) Call(ctx context.Context, task *scheduledTask, action string, wait bool, lease *Lease, metric *MeasurementResult) (taskReply, error) {
	body := map[string]any{"action": action, "region": task.Region.Code, "family": task.Family, "carrier": task.Carrier, "wait": wait}
	if lease != nil {
		body["lease_id"] = lease.LeaseID
	}
	if metric != nil {
		body["measurement"] = metric
		body["retry"] = metric.Status == "failed" && metric.Single == nil
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return taskReply{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/node-task", bytes.NewReader(encoded))
	if err != nil {
		return taskReply{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.tokens[task.Family])
	response, err := c.clients[task.Family].Do(request)
	if err != nil {
		return taskReply{}, errors.New("无法连接节点调度服务")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxLeaseBytes+1))
	if err != nil || len(data) > maxLeaseBytes {
		return taskReply{}, errors.New("节点调度响应无效")
	}
	var status struct {
		Status   string `json:"status"`
		Reason   string `json:"reason"`
		Error    string `json:"error"`
		Retry    bool   `json:"retry"`
		Released bool   `json:"released"`
	}
	if json.Unmarshal(data, &status) != nil {
		return taskReply{}, errors.New("节点调度响应无效")
	}
	if response.StatusCode == 202 && status.Status == "waiting" {
		return taskReply{Waiting: true, Reason: status.Reason}, nil
	}
	if response.StatusCode != 200 {
		return taskReply{}, errors.New(taskErrorText(status.Error))
	}
	if action != "acquire" {
		if !status.Released {
			return taskReply{}, errors.New("节点释放未确认")
		}
		return taskReply{Retry: status.Retry}, nil
	}
	var result Lease
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || result.validate(c.now()) != nil || result.Region.Code != task.Region.Code ||
		result.Family != task.Family || len(result.Targets) != 1 || result.Targets[0].Carrier != task.Carrier || len(result.Targets[0].Candidates) != 1 {
		return taskReply{}, errors.New("节点租约无效")
	}
	return taskReply{Lease: result}, nil
}

func taskErrorText(code string) string {
	switch code {
	case "node_directory_unavailable", "node_verification_failed", "specified_node_unavailable":
		return "当前地区暂无可用测速节点"
	case "task_expired", "task_unavailable":
		return "节点租约已过期"
	case "invalid_session":
		return "测速会话已失效"
	default:
		return "节点调度暂时不可用"
	}
}

type taskScheduler struct {
	control       taskControl
	measure       func(context.Context, Lease, *progressTracker, func() time.Time) Report
	now           func() time.Time
	progress      *progressTracker
	output        io.Writer
	budget        time.Duration
	waited        time.Duration
	finished      int
	warnedRelease bool
	interactive   bool
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *taskScheduler) cancel(task *scheduledTask) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = s.control.Call(ctx, task, "cancel", false, nil, nil)
}

func (s *taskScheduler) acquire(ctx context.Context, task *scheduledTask, wait bool) (taskReply, error) {
	reply, err := s.control.Call(ctx, task, "acquire", wait, nil, nil)
	if err != nil || !reply.Waiting || reply.Reason != "preparing" {
		return reply, err
	}
	// A community firewall gate is already reserved. Finish its preparation now,
	// instead of carrying the reservation while measuring other provinces.
	prepareCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for reply.Waiting && reply.Reason == "preparing" {
		s.progress.Update(s.finished*3, task.label()+" / 等待节点准备")
		if err = sleepContext(prepareCtx, time.Second); err != nil {
			return taskReply{}, err
		}
		reply, err = s.control.Call(prepareCtx, task, "acquire", wait, nil, nil)
		if err != nil {
			return taskReply{}, err
		}
	}
	return reply, nil
}

func (s *taskScheduler) measureTask(ctx context.Context, task *scheduledTask, lease Lease) bool {
	s.progress.Update(s.finished*3, task.label()+" / 连接节点")
	remaining := time.Unix(lease.ExpiresAt, 0).Sub(s.now()) - 3*time.Second
	if remaining > 65*time.Second {
		remaining = 65 * time.Second
	}
	measureCtx, cancel := context.WithTimeout(context.WithValue(ctx, leaseProgressOffset{}, s.finished*3), remaining)
	report := s.measure(measureCtx, lease, s.progress, s.now)
	cancel()
	if len(report.Results) != 1 {
		task.Result.Error = "测速程序未返回结果"
		s.cancel(task)
		return false
	}
	task.Result = report.Results[0]
	// Keep one node's progress inside the complete run's progress scale.
	s.progress.Update(s.finished*3+3, task.label()+" / 释放节点")
	var reply taskReply
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		finishCtx, stop := context.WithTimeout(context.Background(), 4*time.Second)
		reply, err = s.control.Call(finishCtx, task, "finish", false, &lease, &task.Result)
		stop()
		if err == nil && !reply.Waiting {
			return reply.Retry
		}
	}
	if !s.warnedRelease {
		s.warnedRelease = true
		// Printed after the progress tracker stops, so terminal output stays aligned.
	}
	return false
}

func (s *taskScheduler) run(ctx context.Context, tasks []*scheduledTask) {
	for _, task := range tasks {
		if ctx.Err() != nil {
			break
		}
		for attempt := 0; attempt < 2; attempt++ {
			s.progress.Update(s.finished*3, task.label()+" / 获取节点")
			reply, err := s.acquire(ctx, task, false)
			if err != nil {
				task.Result.Error = err.Error()
				s.cancel(task)
				break
			}
			if reply.Waiting {
				task.Pending = true
				task.Result.Error = "节点繁忙"
				if reply.Reason == "source_busy" {
					task.Result.Error = "同一来源已有测速任务"
				}
				break
			}
			if !s.measureTask(ctx, task, reply.Lease) {
				break
			}
		}
		if !task.Pending {
			s.finished++
		}
	}
	pending := make([]*scheduledTask, 0, len(tasks))
	for _, task := range tasks {
		if task.Pending {
			pending = append(pending, task)
		}
	}
	announced := false
	for len(pending) > 0 && s.waited < s.budget && ctx.Err() == nil {
		for index := 0; index < len(pending) && s.waited < s.budget && ctx.Err() == nil; {
			task := pending[index]
			started := time.Now()
			s.progress.Waiting(task.label(), s.waited, s.budget)
			if !announced && !s.interactive {
				fmt.Fprintf(s.output, "[等待] %s 节点繁忙；整次测速累计最多等待 %.0f 秒\n", task.label(), s.budget.Seconds())
				announced = true
			}
			quantum := 3 * time.Second
			if remaining := s.budget - s.waited; remaining < quantum {
				quantum = remaining
			}
			waitCtx, stop := context.WithTimeout(ctx, quantum)
			reply, err := s.acquire(waitCtx, task, true)
			stop()
			s.waited += time.Since(started)
			if err != nil {
				if ctx.Err() != nil || s.waited >= s.budget {
					break
				}
				task.Result.Error = err.Error()
				s.cancel(task)
				pending = append(pending[:index], pending[index+1:]...)
				s.finished++
				continue
			}
			if reply.Waiting {
				index++
				continue
			}
			task.Pending = false
			if s.measureTask(ctx, task, reply.Lease) {
				// Retry only connection/activation failures, never a data transfer.
				task.Pending = true
				index++
				continue
			}
			pending = append(pending[:index], pending[index+1:]...)
			s.finished++
		}
		if len(pending) == 0 || s.waited >= s.budget || ctx.Err() != nil {
			break
		}
		started := time.Now()
		s.progress.Waiting(pending[0].label(), s.waited, s.budget)
		remaining := s.budget - s.waited
		if remaining > 2*time.Second {
			remaining = 2 * time.Second
		}
		_ = sleepContext(ctx, remaining)
		s.waited += time.Since(started)
	}
	for _, task := range pending {
		task.Pending = true
		s.cancel(task)
		s.finished++
	}
	if ctx.Err() != nil {
		for _, task := range tasks {
			s.cancel(task)
		}
	}
}

func runScheduledCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("schedule", flag.ContinueOnError)
	flags.SetOutput(stderr)
	base := flags.String("base", "", "调度服务地址")
	regions := flags.String("regions", "", "省份代码，以逗号分隔")
	names := flags.String("names", "", "省份名称，以逗号分隔")
	families := flags.String("families", "v4", "IP 类型，以逗号分隔")
	carriers := flags.String("carriers", "ct,cu,cm", "运营商代码")
	target := flags.Int("speed", 200, "限速档位")
	token4 := flags.String("session-v4-file", "", "IPv4 会话文件")
	token6 := flags.String("session-v6-file", "", "IPv6 会话文件")
	output := flags.String("output", "", "NDJSON 结果文件")
	diagnosticPath := flags.String("diagnostic-log", "", "JSONL 诊断日志")
	epoch := flags.Int64("reference-time", 0, "平台参考时间")
	if err := flags.Parse(args); err != nil {
		return err
	}
	clock, err := newMeasurementClock(*epoch)
	if err != nil {
		return err
	}
	codes, labels := strings.Split(*regions, ","), strings.Split(*names, ",")
	if len(codes) < 1 || len(codes) > 5 || len(labels) != len(codes) || (*target != 100 && *target != 200 && *target != 400) || *output == "" {
		return errors.New("测速配置无效")
	}
	tokens := map[string]string{}
	for family, path := range map[string]string{"v4": *token4, "v6": *token6} {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return errors.New("无法读取测速会话")
		}
		token := strings.TrimSpace(string(data))
		if !validIdentifier(token, 32, 128) {
			return errors.New("测速会话无效")
		}
		tokens[family] = token
	}
	control, err := newHTTPTaskControl(*base, tokens, clock.Now)
	if err != nil {
		return err
	}
	var tasks []*scheduledTask
	for index, code := range codes {
		if !validIdentifier(code, 2, 4) || strings.TrimSpace(labels[index]) == "" {
			return errors.New("省份无效")
		}
		for _, family := range strings.Split(*families, ",") {
			if tokens[family] == "" {
				return errors.New("缺少 IP 类型对应的测速会话")
			}
			for _, carrier := range strings.Split(*carriers, ",") {
				if !validCarrier(carrier) {
					return errors.New("运营商无效")
				}
				tasks = append(tasks, &scheduledTask{Region: Region{Code: code, Name: labels[index]}, Family: family, Carrier: carrier,
					Result: MeasurementResult{Carrier: carrier, Label: labels[index] + displayCarrier(MeasurementResult{Carrier: carrier}), Status: "failed", Error: "节点繁忙"}})
			}
		}
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, diagnosticFile, err := withDiagnosticLog(signalCtx, *diagnosticPath)
	if err != nil {
		return err
	}
	if diagnosticFile != nil {
		defer diagnosticFile.Close()
	}
	interactive := terminalProgressEnabled()
	progress := newProgressTracker(stderr, len(tasks)*3, "", interactive,
		progressTips(os.Getenv("SPEEDQUALITY_NQ_BINDING_ENABLED") == "1"), os.Getenv("SPEEDQUALITY_TIP_STATE_FILE"))
	scheduler := taskScheduler{control: control, now: clock.Now, measure: runLease, progress: progress,
		output: stderr, budget: queueWaitBudget, interactive: interactive}
	scheduler.run(ctx, tasks)
	progress.Finish()
	if scheduler.warnedRelease {
		fmt.Fprintln(stderr, "[!] 部分节点释放未获确认，将由短期租约自动回收")
	}
	if scheduler.waited >= scheduler.budget {
		fmt.Fprintln(stderr, "[!] 已达到 30 秒等待上限，跳过仍繁忙的项目")
	}
	var idBytes [12]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return err
	}
	var reports []Report
	byGroup := map[string]int{}
	for _, task := range tasks {
		key := task.Region.Code + "/" + task.Family
		index, ok := byGroup[key]
		if !ok {
			index = len(reports)
			byGroup[key] = index
			reports = append(reports, Report{Version: 1, LeaseID: "managed_" + hex.EncodeToString(idBytes[:]),
				Region: task.Region, Family: task.Family, DurationSeconds: 5, TargetMbps: *target, Modes: []string{"s"},
				StartedAt: clock.reference.Unix(), CompletedAt: clock.Now().Unix()})
		}
		reports[index].Results = append(reports[index].Results, task.Result)
	}
	previousRegion := ""
	succeeded := false
	for index, report := range reports {
		if report.Region.Code != previousRegion {
			fmt.Fprintf(stdout, "\n%s%s%s\n", cyan, report.Region.Name, reset)
			previousRegion = report.Region.Code
		}
		printReport(stdout, report)
		for _, result := range report.Results {
			if result.Status == "ok" {
				succeeded = true
			}
		}
		data, err := json.Marshal(report)
		if err != nil {
			return err
		}
		if err = writeResult(*output, data, index > 0); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return errors.New("测速已取消")
	}
	if !succeeded {
		return errors.New("未取得完整测速结果；请调整地区、IP 类型或稍后重试")
	}
	return nil
}
