package downloadd

// 巡检补充单测：任务库并发压测（多 goroutine 混合 Insert/UpdatePoll/SetStatus/FinishTask/ListTasks，
// WAL+busy_timeout 下不丢写不死锁）与终态语义（FinishTask 后不再进轮询集、terminal 判定）。

import (
	"fmt"
	"sync"
	"testing"
)

func TestDBConcurrentMixedOps(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const writers = 8
	const perWriter = 25
	ids := make(chan int64, writers*perWriter)

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				task := &Task{
					Engine: EngineAria2, Gid: fmt.Sprintf("g-%d-%d", w, i),
					URL: "http://x", Name: fmt.Sprintf("f%d-%d", w, i),
					SaveDir: dir, Status: StatusActive,
				}
				if err := db.InsertTask(task); err != nil {
					t.Errorf("insert: %v", err)
					return
				}
				ids <- task.ID
				if err := db.UpdatePoll(task.ID, StatusActive, 1000, 100, 50, 10, ""); err != nil {
					t.Errorf("updatePoll: %v", err)
				}
				if i%3 == 0 {
					if err := db.FinishTask(task.ID, StatusDone, 1000, 1000, 100, ""); err != nil {
						t.Errorf("finish: %v", err)
					}
				} else {
					if err := db.SetStatus(task.ID, StatusPaused, ""); err != nil {
						t.Errorf("setStatus: %v", err)
					}
				}
			}
		}(w)
	}
	// 并发读
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			if _, err := db.ListTasks(); err != nil {
				t.Errorf("list: %v", err)
				return
			}
		}
	}()
	wg.Wait()
	close(ids)

	// 全部写入应可见（无丢写）
	all, err := db.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != writers*perWriter {
		t.Fatalf("丢写：期望 %d 条，实际 %d", writers*perWriter, len(all))
	}
	// i%3==0 的已完成：终态不进轮询集
	poll, err := db.ListPollTasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range poll {
		if p.Status == StatusDone {
			t.Fatalf("终态任务不应出现在轮询集: gid=%s", p.Gid)
		}
	}
	// 数据库完整性
	var integrity string
	row := db.sql.QueryRow("PRAGMA integrity_check")
	if err := row.Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity_check=%q err=%v", integrity, err)
	}
}

func TestTaskTerminal(t *testing.T) {
	cases := map[string]struct {
		status string
		want   bool
	}{
		StatusDone:    {StatusDone, true},
		StatusError:   {StatusError, true},
		StatusActive:  {StatusActive, false},
		StatusPending: {StatusPending, false},
		StatusPaused:  {StatusPaused, false},
	}
	for status, c := range cases {
		task := &Task{Status: status}
		if task.terminal() != c.want {
			t.Errorf("terminal(%s) = %v, want %v", status, task.terminal(), c.want)
		}
	}
}

func TestDBFinishThenUpdateIsOrdered(t *testing.T) {
	// FinishTask 之后的 SetStatus 不应把终态改回非终态（轮询只取非终态，此处验证语义边界）：
	// 库层允许写（供人工修复），但 ListPollTasks 必须继续排除它。
	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task := &Task{Engine: EngineFTP, URL: "ftp://x/f", Name: "f", SaveDir: dir, Status: StatusActive}
	if err := db.InsertTask(task); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishTask(task.ID, StatusError, 500, 100, 20, "网络断开"); err != nil {
		t.Fatal(err)
	}
	// 误操作：对终态再 SetStatus
	if err := db.SetStatus(task.ID, StatusActive, ""); err != nil {
		t.Fatal(err)
	}
	poll, err := db.ListPollTasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range poll {
		if p.ID == task.ID {
			t.Fatal("被置回 active 的终态任务进入了轮询集（ListPollTasks 过滤逻辑应只认 status）——" +
				"若此处失败说明过滤依据 terminal() 与写入不一致")
		}
	}
}
