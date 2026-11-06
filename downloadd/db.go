// Package downloadd：普通下载引擎层（网盘下载之外的 HTTP/磁力/BT/种子/ed2k/Metalink/FTP）。
//
// 引擎分工：aria2-next（外部进程，JSON-RPC）负责 HTTP/磁力/BT/种子/ed2k/Metalink；
// Go 内置（jlaffaye/ftp）负责 FTP/FTPS。任务存独立 SQLite（<dataDir>/downloads.db），
// 与核心（Java/Kotlin）的 yunx.db 完全分家，互不影响。
package downloadd

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// 任务状态机：pending(排队) → active(下载中) → done / error；任意非终态可 paused。
const (
	StatusPending = "pending"
	StatusActive  = "active"
	StatusPaused  = "paused"
	StatusDone    = "done"
	StatusError   = "error"
)

// 引擎标识（normal_task.engine 列）。
const (
	EngineAria2 = "aria2"
	EngineFTP   = "ftp"
)

// Task 普通下载任务（表 normal_task 的一行）。JSON tag 直接供 NormalDownloadList 输出。
type Task struct {
	ID         int64  `json:"id"`
	Engine     string `json:"engine"`
	Gid        string `json:"gid"`
	URL        string `json:"url"`
	Name       string `json:"name"`
	SaveDir    string `json:"saveDir"`
	SavePath   string `json:"savePath"`
	Size       int64  `json:"size"`
	Downloaded int64  `json:"downloaded"`
	Progress   int    `json:"progress"`
	Speed      int64  `json:"speed"`
	Status     string `json:"status"`
	Err        string `json:"err"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
}

// terminal 终态判定（轮询与队列调度都用）。
func (t *Task) terminal() bool {
	return t.Status == StatusDone || t.Status == StatusError
}

// DB 任务库句柄。modernc.org/sqlite 纯 Go 驱动（无 cgo，跨平台构建友好）。
type DB struct {
	sql *sql.DB
}

// OpenDB 打开/创建 <dataDir>/downloads.db（WAL 模式，busy 超时 5s），并建表。
func OpenDB(dataDir string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)",
		filepath.Join(dataDir, "downloads.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开任务库: %w", err)
	}
	// SQLite 单写者：限 1 连接避免偶发 SQLITE_BUSY
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("建表: %w", err)
	}
	return &DB{sql: db}, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS normal_task (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	engine     TEXT    NOT NULL DEFAULT '',
	gid        TEXT    NOT NULL DEFAULT '',
	url        TEXT    NOT NULL DEFAULT '',
	name       TEXT    NOT NULL DEFAULT '',
	save_dir   TEXT    NOT NULL DEFAULT '',
	save_path  TEXT    NOT NULL DEFAULT '',
	size       INTEGER NOT NULL DEFAULT 0,
	downloaded INTEGER NOT NULL DEFAULT 0,
	progress   INTEGER NOT NULL DEFAULT 0,
	speed      INTEGER NOT NULL DEFAULT 0,
	status     TEXT    NOT NULL DEFAULT 'pending',
	err        TEXT    NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_normal_task_created ON normal_task(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_normal_task_engine_status ON normal_task(engine, status);
`

// Close 关闭底层数据库连接。
func (d *DB) Close() error { return d.sql.Close() }

// InsertTask 新建任务（status 缺省 pending，时间戳自动填），回填自增 id。
func (d *DB) InsertTask(t *Task) error {
	if t.Status == "" {
		t.Status = StatusPending
	}
	now := time.Now().Unix()
	t.CreatedAt, t.UpdatedAt = now, now
	res, err := d.sql.Exec(
		`INSERT INTO normal_task (engine, gid, url, name, save_dir, save_path, size, downloaded, progress, speed, status, err, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.Engine, t.Gid, t.URL, t.Name, t.SaveDir, t.SavePath, t.Size, t.Downloaded, t.Progress, t.Speed, t.Status, t.Err, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return fmt.Errorf("插入任务: %w", err)
	}
	t.ID, err = res.LastInsertId()
	return err
}

// UpdateTask 整行更新（按 id；updated_at 自动刷新）。
func (d *DB) UpdateTask(t *Task) error {
	t.UpdatedAt = time.Now().Unix()
	_, err := d.sql.Exec(
		`UPDATE normal_task SET engine=?, gid=?, url=?, name=?, save_dir=?, save_path=?, size=?, downloaded=?, progress=?, speed=?, status=?, err=?, updated_at=? WHERE id=?`,
		t.Engine, t.Gid, t.URL, t.Name, t.SaveDir, t.SavePath, t.Size, t.Downloaded, t.Progress, t.Speed, t.Status, t.Err, t.UpdatedAt, t.ID)
	return err
}

// UpdateProgress 轮询/传输进度回写（不动 status/err）。
func (d *DB) UpdateProgress(id int64, size, downloaded, speed int64, progress int) error {
	_, err := d.sql.Exec(
		`UPDATE normal_task SET size=?, downloaded=?, speed=?, progress=?, updated_at=? WHERE id=?`,
		size, downloaded, speed, progress, time.Now().Unix(), id)
	return err
}

// SetStatus 状态迁移（err 可空；非下载中状态顺手清 speed）。
func (d *DB) SetStatus(id int64, status, errMsg string) error {
	_, err := d.sql.Exec(`UPDATE normal_task SET status=?, err=?, speed=0, updated_at=? WHERE id=?`,
		status, errMsg, time.Now().Unix(), id)
	return err
}

// FinishTask 终态回写：状态 + 进度快照（done/error 用）。
func (d *DB) FinishTask(id int64, status string, size, downloaded int64, progress int, errMsg string) error {
	_, err := d.sql.Exec(
		`UPDATE normal_task SET status=?, err=?, size=?, downloaded=?, progress=?, speed=0, updated_at=? WHERE id=?`,
		status, errMsg, size, downloaded, progress, time.Now().Unix(), id)
	return err
}

// UpdatePoll 轮询非终态回写：状态与进度一次写齐（避免 UpdateProgress/SetStatus 两跳互相清字段）。
func (d *DB) UpdatePoll(id int64, status string, size, downloaded, speed int64, progress int, errMsg string) error {
	_, err := d.sql.Exec(
		`UPDATE normal_task SET status=?, err=?, size=?, downloaded=?, speed=?, progress=?, updated_at=? WHERE id=?`,
		status, errMsg, size, downloaded, speed, progress, time.Now().Unix(), id)
	return err
}

// SetSavePath 补填落盘路径与文件名（aria2 complete 后 files[0].path）。
func (d *DB) SetSavePath(id int64, savePath, name string) error {
	_, err := d.sql.Exec(`UPDATE normal_task SET save_path=?, name=?, updated_at=? WHERE id=?`,
		savePath, name, time.Now().Unix(), id)
	return err
}

// GetTask 按 id 取任务；不存在返回 nil, nil。
func (d *DB) GetTask(id int64) (*Task, error) {
	rows, err := d.sql.Query(`SELECT id, engine, gid, url, name, save_dir, save_path, size, downloaded, progress, speed, status, err, created_at, updated_at FROM normal_task WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanTask(rows)
}

// ListTasks 全量列表（created_at 倒序，同秒按 id 倒序——最新提交在最前）。
func (d *DB) ListTasks() ([]Task, error) {
	rows, err := d.sql.Query(`SELECT id, engine, gid, url, name, save_dir, save_path, size, downloaded, progress, speed, status, err, created_at, updated_at FROM normal_task ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ListPollTasks 待轮询任务：有 gid 且非终态（aria2 轮询用）。
func (d *DB) ListPollTasks() ([]Task, error) {
	rows, err := d.sql.Query(`SELECT id, engine, gid, url, name, save_dir, save_path, size, downloaded, progress, speed, status, err, created_at, updated_at FROM normal_task WHERE gid<>'' AND status IN ('pending','active','paused')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ListEngineTasks 指定引擎的任务（批量操作用）。
func (d *DB) ListEngineTasks(engine string) ([]Task, error) {
	rows, err := d.sql.Query(`SELECT id, engine, gid, url, name, save_dir, save_path, size, downloaded, progress, speed, status, err, created_at, updated_at FROM normal_task WHERE engine=? ORDER BY id`, engine)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// DeleteTask 删行（remove 语义：不删已下载文件）。
func (d *DB) DeleteTask(id int64) error {
	_, err := d.sql.Exec(`DELETE FROM normal_task WHERE id=?`, id)
	return err
}

func scanTask(rows *sql.Rows) (*Task, error) {
	var t Task
	if err := rows.Scan(&t.ID, &t.Engine, &t.Gid, &t.URL, &t.Name, &t.SaveDir, &t.SavePath,
		&t.Size, &t.Downloaded, &t.Progress, &t.Speed, &t.Status, &t.Err, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}
