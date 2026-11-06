package downloadd

import (
	"testing"
)

func TestDBCRUDRoundtrip(t *testing.T) {
	db, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// 插入三条（aria2 两条 + ftp 一条）
	t1 := &Task{Engine: EngineAria2, Gid: "gid1", URL: "http://h/a", Name: "a", SaveDir: "/tmp/dl", Status: StatusPending}
	if err := db.InsertTask(t1); err != nil {
		t.Fatal(err)
	}
	if t1.ID == 0 {
		t.Fatal("自增 id 未回填")
	}
	t2 := &Task{Engine: EngineAria2, Gid: "gid2", URL: "magnet:?xt=x", Status: StatusActive}
	db.InsertTask(t2)
	t3 := &Task{Engine: EngineFTP, URL: "ftp://h/f", Status: StatusPending}
	db.InsertTask(t3)

	// Get 往返
	got, err := db.GetTask(t1.ID)
	if err != nil || got == nil {
		t.Fatalf("GetTask: %v %v", got, err)
	}
	if got.Gid != "gid1" || got.Name != "a" || got.Status != StatusPending || got.SaveDir != "/tmp/dl" {
		t.Errorf("往返字段丢失: %+v", got)
	}
	if got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Error("时间戳未填")
	}
	if _, err := db.GetTask(99999); err != nil || got == nil {
		t.Errorf("不存在的 id 应返回 nil,nil")
	}

	// 进度回写
	if err := db.UpdatePoll(t1.ID, StatusActive, 1000, 400, 12345, 40, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetTask(t1.ID)
	if got.Size != 1000 || got.Downloaded != 400 || got.Speed != 12345 || got.Progress != 40 || got.Status != StatusActive {
		t.Errorf("UpdatePoll 后字段不符: %+v", got)
	}

	// 终态
	if err := db.FinishTask(t1.ID, StatusDone, 1000, 1000, 100, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetTask(t1.ID)
	if got.Status != StatusDone || got.Speed != 0 || got.Progress != 100 {
		t.Errorf("FinishTask 后字段不符: %+v", got)
	}

	// SetSavePath
	db.SetSavePath(t1.ID, "/tmp/dl/a", "a")
	got, _ = db.GetTask(t1.ID)
	if got.SavePath != "/tmp/dl/a" || got.Name != "a" {
		t.Errorf("SetSavePath 后字段不符: %+v", got)
	}

	// 列表（created_at 倒序；同秒插入按 id 倒序 → t3,t2,t1）
	list, err := db.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("列表长度 = %d, want 3", len(list))
	}
	if list[0].ID != t3.ID || list[1].ID != t2.ID || list[2].ID != t1.ID {
		t.Errorf("列表顺序不对: %d,%d,%d", list[0].ID, list[1].ID, list[2].ID)
	}

	// 待轮询集合：有 gid 且非终态 → 只剩 gid2
	polls, err := db.ListPollTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(polls) != 1 || polls[0].Gid != "gid2" {
		t.Errorf("ListPollTasks = %+v, want 只含 gid2", polls)
	}

	// 引擎过滤
	ftpTasks, _ := db.ListEngineTasks(EngineFTP)
	if len(ftpTasks) != 1 || ftpTasks[0].ID != t3.ID {
		t.Errorf("ListEngineTasks(ftp) = %+v", ftpTasks)
	}

	// 整行更新 + 删除
	t3.Status = StatusPaused
	if err := db.UpdateTask(t3); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetTask(t3.ID)
	if got.Status != StatusPaused {
		t.Errorf("UpdateTask 未生效: %+v", got)
	}
	if err := db.DeleteTask(t3.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetTask(t3.ID); got != nil || err != nil {
		t.Errorf("删除后应查不到")
	}
	list, _ = db.ListTasks()
	if len(list) != 2 {
		t.Errorf("删除后列表长度 = %d, want 2", len(list))
	}
}
