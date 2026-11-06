package downloadd

// 巡检补充单测：pollTask 状态映射全表驱动——httptest 假 aria2 RPC + 临时任务库，
// 覆盖 active/waiting/paused/complete/error(带/不带 message)/removed/GID 消失各分支。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newFakeAria2 起一个假 aria2 JSON-RPC：fixtures 按 gid 返回 tellStatus 结果；
// rpcOK=false 时 tellStatus 返回 JSON-RPC error（模拟 GID 不存在，但 getVersion 仍活）。
func newFakeAria2(t *testing.T, fixtures map[string]string, rpcOK bool) (*Aria2, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "aria2.getVersion":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "yunx", "jsonrpc": "2.0", "result": map[string]any{"version": "fake"},
			})
		case "aria2.tellStatus":
			gid, _ := req.Params[1].(string)
			if body, ok := fixtures[gid]; ok && rpcOK {
				_, _ = w.Write([]byte(`{"id":"yunx","jsonrpc":"2.0","result":` + body + `}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"yunx","jsonrpc":"2.0","error":{"code":1,"message":"GID not found"}}`))
		default:
			_, _ = w.Write([]byte(`{"id":"yunx","jsonrpc":"2.0","result":"OK"}`))
		}
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := &Aria2{
		db:      db,
		dataDir: dir,
		hooks:   Aria2Hooks{},
		client:  srv.Client(),
		running: true,
		port:    portOfURL(t, srv.URL),
		secret:  "test-secret",
	}
	return a, srv
}

func portOfURL(t *testing.T, raw string) int {
	t.Helper()
	var host string
	var p int
	if _, err := fmt.Sscanf(raw, "http://%s", &host); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Sscanf(host, "127.0.0.1:%d", &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPollTaskStatusMapping(t *testing.T) {
	fixtures := map[string]string{
		"g-active":   `{"gid":"g-active","status":"active","totalLength":"1000","completedLength":"250","downloadSpeed":"500"}`,
		"g-waiting":  `{"gid":"g-waiting","status":"waiting","totalLength":"1000","completedLength":"0","downloadSpeed":"0"}`,
		"g-paused":   `{"gid":"g-paused","status":"paused","totalLength":"1000","completedLength":"400","downloadSpeed":"0"}`,
		"g-complete": `{"gid":"g-complete","status":"complete","totalLength":"1000","completedLength":"1000","downloadSpeed":"0","files":[{"path":"/tmp/dl/done.mp4"}]}`,
		"g-errmsg":   `{"gid":"g-errmsg","status":"error","totalLength":"100","completedLength":"10","errorMessage":"seed 坏了"}`,
		"g-errcode":  `{"gid":"g-errcode","status":"error","totalLength":"0","completedLength":"0","errorCode":"13"}`,
		"g-removed":  `{"gid":"g-removed","status":"removed","totalLength":"0","completedLength":"0"}`,
	}
	a, _ := newFakeAria2(t, fixtures, true)
	for gid := range fixtures {
		if err := a.db.InsertTask(&Task{
			Engine: EngineAria2, Gid: gid, URL: "http://x/" + gid, Name: gid,
			SaveDir: "/tmp/dl", Status: StatusActive,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range mustPollTasks(t, a) {
		a.pollTask(&task)
	}
	for gid, want := range map[string]struct {
		status string
		prog   int
		errMsg string
	}{
		"g-active":   {StatusActive, 25, ""},
		"g-waiting":  {StatusPending, 0, ""},
		"g-paused":   {StatusPaused, 40, ""},
		"g-complete": {StatusDone, 100, ""},
		"g-errmsg":   {StatusError, 10, "seed 坏了"},
		"g-errcode":  {StatusError, 0, "下载失败（errorCode=13）"},
		"g-removed":  {StatusError, 0, "任务已被 aria2 移除"},
	} {
		got := mustTaskByGid(t, a, gid)
		if got.Status != want.status || got.Progress != want.prog {
			t.Errorf("%s: status=%s prog=%d, want %s/%d", gid, got.Status, got.Progress, want.status, want.prog)
		}
		if want.errMsg != "" && got.Err != want.errMsg {
			t.Errorf("%s: err=%q, want %q", gid, got.Err, want.errMsg)
		}
	}
	// complete 应回填 files[0].path 到 save_path
	if got := mustTaskByGid(t, a, "g-complete"); got.SavePath != "/tmp/dl/done.mp4" {
		t.Errorf("complete 未回填 save_path: %q", got.SavePath)
	}
	// active 的 speed 应回写（250/1000=25%，速度 500）
	if got := mustTaskByGid(t, a, "g-active"); got.Speed != 500 || got.Downloaded != 250 {
		t.Errorf("active 进度字段错误: speed=%d downloaded=%d", got.Speed, got.Downloaded)
	}
}

func TestPollTaskGidGone(t *testing.T) {
	// RPC 活着（getVersion OK）但 GID 不存在 → 任务置 error「任务不存在于 aria2」
	a, _ := newFakeAria2(t, nil, false)
	if err := a.db.InsertTask(&Task{
		Engine: EngineAria2, Gid: "g-ghost", URL: "http://x", Name: "ghost",
		SaveDir: "/tmp/dl", Status: StatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	tasks := mustPollTasks(t, a)
	a.pollTask(&tasks[0])
	got := mustTaskByGid(t, a, "g-ghost")
	if got.Status != StatusError {
		t.Fatalf("GID 消失应置 error，实际 %s", got.Status)
	}
}

func mustPollTasks(t *testing.T, a *Aria2) []Task {
	t.Helper()
	tasks, err := a.db.ListPollTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) == 0 {
		t.Fatal("无待轮询任务")
	}
	return tasks
}

func mustTaskByGid(t *testing.T, a *Aria2, gid string) *Task {
	t.Helper()
	tasks, err := a.db.ListPollTasks()
	if err != nil {
		t.Fatal(err)
	}
	all, err := a.db.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	_ = tasks
	for _, tt := range all {
		if tt.Gid == gid {
			return &tt
		}
	}
	t.Fatalf("找不到 gid=%s", gid)
	return nil
}
