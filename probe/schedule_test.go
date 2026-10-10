package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type taskControlFunc func(context.Context, *scheduledTask, string, bool, *Lease, *MeasurementResult) (taskReply, error)

func (f taskControlFunc) Call(ctx context.Context, task *scheduledTask, action string, wait bool, lease *Lease, metric *MeasurementResult) (taskReply, error) {
	return f(ctx, task, action, wait, lease, metric)
}

func schedulerTask(region, family string) *scheduledTask {
	return &scheduledTask{Region: Region{Code: region, Name: region}, Family: family, Carrier: "ct",
		Result: MeasurementResult{Carrier: "ct", Label: region + "电信", Status: "failed", Error: "节点繁忙"}}
}

func fakeTaskLease(task *scheduledTask) Lease {
	lease := validTestLease(time.Now())
	lease.Region = task.Region
	lease.Family = task.Family
	return lease
}

func fakeTaskMeasure(_ context.Context, lease Lease, _ *progressTracker, _ func() time.Time) Report {
	return Report{Results: []MeasurementResult{{Carrier: "ct", Label: lease.Region.Name + "电信", Status: "ok",
		NodeID: lease.Targets[0].Candidates[0].ID, Single: &ModeResult{UploadMbps: 200, DownloadMbps: 200}}}}
}

func quietScheduler(control taskControl) taskScheduler {
	return taskScheduler{control: control, measure: fakeTaskMeasure, now: time.Now, output: io.Discard,
		budget: 100 * time.Millisecond, progress: newProgressTracker(io.Discard, 90, "", false, nil, "")}
}

func TestSchedulerDefersBusyBeijingAndReleasesEveryMeasuredNode(t *testing.T) {
	var events []string
	control := taskControlFunc(func(_ context.Context, task *scheduledTask, action string, wait bool, _ *Lease, _ *MeasurementResult) (taskReply, error) {
		events = append(events, task.Region.Code+":"+action)
		if action != "acquire" {
			return taskReply{}, nil
		}
		if task.Region.Code == "bj" && !wait {
			return taskReply{Waiting: true, Reason: "node_capacity_exhausted"}, nil
		}
		return taskReply{Lease: fakeTaskLease(task)}, nil
	})
	tasks := []*scheduledTask{schedulerTask("bj", "v4"), schedulerTask("sh", "v4"), schedulerTask("gd", "v4")}
	scheduler := quietScheduler(control)
	scheduler.run(context.Background(), tasks)
	want := "bj:acquire,sh:acquire,sh:finish,gd:acquire,gd:finish,bj:acquire,bj:finish"
	if strings.Join(events, ",") != want {
		t.Fatalf("events: %v", events)
	}
	for _, task := range tasks {
		if task.Result.Status != "ok" {
			t.Fatalf("unfinished: %+v", task)
		}
	}
	if tasks[0].Region.Code != "bj" {
		t.Fatal("report order changed with execution order")
	}
}

func TestSchedulerSharesOneWaitBudgetAcrossProvincesAndFamilies(t *testing.T) {
	var tickets, cancels int
	control := taskControlFunc(func(_ context.Context, _ *scheduledTask, action string, wait bool, _ *Lease, _ *MeasurementResult) (taskReply, error) {
		if action == "cancel" {
			cancels++
			return taskReply{}, nil
		}
		if wait {
			tickets++
		}
		return taskReply{Waiting: true, Reason: "node_capacity_exhausted"}, nil
	})
	var tasks []*scheduledTask
	for _, region := range []string{"bj", "sh", "gd"} {
		for _, family := range []string{"v4", "v6"} {
			tasks = append(tasks, schedulerTask(region, family))
		}
	}
	scheduler := quietScheduler(control)
	scheduler.budget = 35 * time.Millisecond
	started := time.Now()
	scheduler.run(context.Background(), tasks)
	if duration := time.Since(started); duration > 300*time.Millisecond {
		t.Fatalf("budget restarted: %v", duration)
	}
	if scheduler.waited < scheduler.budget || tickets < 6 || cancels != 6 {
		t.Fatalf("wait=%v, tickets=%d, cancels=%d", scheduler.waited, tickets, cancels)
	}
}

func TestSchedulerWaitBudgetAlsoBoundsSlowPollingRequests(t *testing.T) {
	control := taskControlFunc(func(ctx context.Context, _ *scheduledTask, action string, wait bool, _ *Lease, _ *MeasurementResult) (taskReply, error) {
		if action == "cancel" {
			return taskReply{}, nil
		}
		if wait {
			<-ctx.Done()
			return taskReply{}, ctx.Err()
		}
		return taskReply{Waiting: true}, nil
	})
	scheduler := quietScheduler(control)
	scheduler.budget = 20 * time.Millisecond
	started := time.Now()
	scheduler.run(context.Background(), []*scheduledTask{schedulerTask("bj", "v4")})
	if time.Since(started) > 200*time.Millisecond {
		t.Fatal("poll exceeded shared wait deadline")
	}
}

func TestSchedulerDoesNotRepeatFailedTransfers(t *testing.T) {
	acquires := 0
	control := taskControlFunc(func(_ context.Context, task *scheduledTask, action string, _ bool, _ *Lease, _ *MeasurementResult) (taskReply, error) {
		if action == "acquire" {
			acquires++
			return taskReply{Lease: fakeTaskLease(task)}, nil
		}
		return taskReply{}, nil
	})
	scheduler := quietScheduler(control)
	scheduler.measure = func(_ context.Context, _ Lease, _ *progressTracker, _ func() time.Time) Report {
		return Report{Results: []MeasurementResult{{Status: "failed", Single: &ModeResult{DownloadBytes: 100}}}}
	}
	scheduler.run(context.Background(), []*scheduledTask{schedulerTask("bj", "v4")})
	if acquires != 1 {
		t.Fatalf("repeated transfer %d times", acquires)
	}
}

func TestTaskHTTPRejectsRedirectsAndMismatchedLeases(t *testing.T) {
	task := schedulerTask("hb", "v4")
	var requestBody map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/node-task" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 40) {
			t.Error("invalid control request")
		}
		_ = json.NewDecoder(r.Body).Decode(&requestBody)
		lease := fakeTaskLease(task)
		lease.Region.Code = "bj"
		_ = json.NewEncoder(w).Encode(lease)
	}))
	defer server.Close()
	control, err := newHTTPTaskControl(server.URL, map[string]string{"v4": strings.Repeat("a", 40)}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if control.clients["v4"].CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("credentials may follow a redirect")
	}
	control.clients["v4"] = server.Client()
	if _, err := control.Call(context.Background(), task, "acquire", true, nil, nil); err == nil {
		t.Fatal("accepted a lease for another province")
	}
	if requestBody["carrier"] != "ct" || requestBody["wait"] != true {
		t.Fatalf("request: %+v", requestBody)
	}
}

func TestBusyResultsAreCompactAndWaitingHasNoFakeProgress(t *testing.T) {
	var text bytes.Buffer
	printReport(&text, Report{Family: "v4", Results: []MeasurementResult{{Carrier: "ct", Status: "failed", Error: "节点繁忙"}}})
	if strings.Contains(text.String(), "失败") || strings.Contains(text.String(), "[失败]") {
		t.Fatalf("busy is a network failure: %s", text.String())
	}
	tracker := newProgressTracker(&text, 3, "", false, nil, "")
	tracker.Waiting("北京电信 IPv4", 8*time.Second, queueWaitBudget)
	tracker.render()
	if !strings.Contains(text.String(), "累计等待 8 秒 / 最多 30 秒") {
		t.Fatalf("missing countdown: %s", text.String())
	}
}
