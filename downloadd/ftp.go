// ftp.go：FTP/FTPS 下载引擎（Go 内置，jlaffaye/ftp）——aria2-next 不支持 FTP，这里补位。
//
// 模型：队列 + 最多 2 个并发 worker。断点续传：本地已存在文件按大小 offset 走 RetrFrom 追加，
// 完成时字节数必须等于服务器 Size 才算 done。防挂死三件套：
//   1. 连接/登录 15s 超时；2. 数据连接每次读前推 60s idle deadline（读挂死即超时）；
//   3. 单任务总时长 30 分钟，且 watchdog 持有底层 conn 引用——暂停/超时可强制解阻塞
//      控制连接上的任何挂起读（jlaffaye 未暴露 ServerConn.SetDeadline，经 DialFunc 包一层拿到）。
package downloadd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"
)

const (
	ftpLimitTickDefault = 100 * time.Millisecond
	ftpWorkers       = 2                // 并发下载 worker 数
	ftpDialTimeout   = 15 * time.Second // 连接/登录超时
	ftpIdleTimeout   = 60 * time.Second // 数据连接读空闲超时
	ftpTotalTimeout  = 30 * time.Minute // 单任务总时长上限
	ftpReportEvery   = time.Second      // DB 进度回写间隔
	ftpQueueCapacity = 256
)

// 传输中断的内部哨兵（区分暂停/移除/真错误）。
var (
	errFTPPaused  = errors.New("任务已暂停")
	errFTPRemoved = errors.New("任务已移除")
)

// ftpConns 持有一次传输的底层连接引用，供 watchdog 强制解阻塞。
type ftpConns struct {
	mu   sync.Mutex
	ctrl net.Conn       // 控制连接的原始 TCP/TLS conn
	data *ftp.Response  // 当前数据连接
}

func (c *ftpConns) setCtrl(conn net.Conn) {
	c.mu.Lock()
	c.ctrl = conn
	c.mu.Unlock()
}

func (c *ftpConns) setData(r *ftp.Response) {
	c.mu.Lock()
	c.data = r
	c.mu.Unlock()
}

// interrupt 把两条连接的 deadline 置为过去：任何挂起读/写立即以超时错误返回。
func (c *ftpConns) interrupt() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctrl != nil {
		_ = c.ctrl.SetDeadline(time.Now())
	}
	if c.data != nil {
		_ = c.data.SetDeadline(time.Now())
	}
}

// ftpCtl 运行中任务的控制柄。
type ftpCtl struct {
	cancel context.CancelFunc
	remove bool
	pause  bool
}

// FTPEngine FTP 队列引擎。
type FTPEngine struct {
	db        *DB
	queue     chan int64
	stopCh    chan struct{}
	settingsFn func() int64 // 全局限速（字节/秒，0=不限）；Manager 注入，每 tick 拉一次
	wg     sync.WaitGroup

	mu      sync.Mutex
	running map[int64]*ftpCtl // worker 正在处理的任务
	started bool
}

// NewFTPEngine 构造并启动 worker 池（幂等）。
func NewFTPEngine(db *DB) *FTPEngine {
	e := &FTPEngine{
		db:      db,
		queue:   make(chan int64, ftpQueueCapacity),
		stopCh:  make(chan struct{}),
		running: make(map[int64]*ftpCtl),
	}
	e.Start()
	return e
}

// Start 启动 worker 池（幂等）。
func (e *FTPEngine) Start() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return
	}
	e.started = true
	for i := 0; i < ftpWorkers; i++ {
		e.wg.Add(1)
		go e.worker()
	}
}

// Stop 停止所有 worker 与运行中任务（应用退出路径；运行中任务置 paused 等待下次续传）。
func (e *FTPEngine) Stop() {
	e.mu.Lock()
	if !e.started {
		e.mu.Unlock()
		return
	}
	e.started = false
	ctls := make([]*ftpCtl, 0, len(e.running))
	for _, c := range e.running {
		ctls = append(ctls, c)
	}
	close(e.stopCh)
	e.mu.Unlock()
	for _, c := range ctls {
		c.cancel()
	}
	e.wg.Wait()
}

func (e *FTPEngine) worker() {
	defer e.wg.Done()
	for {
		select {
		case <-e.stopCh:
			return
		case id := <-e.queue:
			if !e.process(id) {
				return
			}
		}
	}
}

// process 取出队列头的任务执行；返回 false 表示引擎已停。
func (e *FTPEngine) process(id int64) bool {
	e.mu.Lock()
	started := e.started
	e.mu.Unlock()
	if !started {
		return false
	}

	t, err := e.db.GetTask(id)
	if err != nil || t == nil {
		return true // 行没了（被移除）→ 丢弃
	}
	if t.Engine != EngineFTP || t.Status != StatusPending {
		return true // 暂停/已完成等 → 丢弃（resume 会重新入队）
	}

	ctx, cancel := context.WithCancel(context.Background())
	ctl := &ftpCtl{cancel: cancel}
	e.mu.Lock()
	if !e.started {
		e.mu.Unlock()
		cancel()
		return false
	}
	e.running[id] = ctl
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		delete(e.running, id)
		e.mu.Unlock()
	}()

	// 双检：注册控制柄后再核一次状态，堵「Pause 落在出队与注册之间」的窗口
	//（那时 Pause 走的是 DB 置 paused 分支，这里能看到）。
	if t2, err := e.db.GetTask(id); err != nil || t2 == nil || t2.Status != StatusPending {
		return true
	}

	err = e.download(ctx, t)
	switch {
	case err == nil:
		_ = e.db.FinishTask(id, StatusDone, t.Size, t.Size, 100, "")
		_ = e.db.SetSavePath(id, t.SavePath, t.Name)
	case errors.Is(err, errFTPPaused):
		_ = e.db.SetStatus(id, StatusPaused, "")
	case errors.Is(err, errFTPRemoved):
		_ = e.db.DeleteTask(id)
	default:
		_ = e.db.SetStatus(id, StatusError, err.Error())
	}
	return true
}

// Submit 新建 FTP 任务并入队。返回任务 id。
func (e *FTPEngine) Submit(rawURL, saveDir, name string) (int64, error) {
	t := &Task{
		Engine:  EngineFTP,
		URL:     rawURL,
		Name:    name,
		SaveDir: saveDir,
		Status:  StatusPending,
	}
	if t.Name != "" {
		t.SavePath = filepath.Join(saveDir, sanitizeFileName(t.Name))
	}
	if err := e.db.InsertTask(t); err != nil {
		return 0, err
	}
	e.Enqueue(t.ID)
	return t.ID, nil
}

// Enqueue 入队（Submit/Resume 共用）。队列满时直接置 error（极端防御）。
func (e *FTPEngine) Enqueue(id int64) {
	select {
	case e.queue <- id:
	default:
		_ = e.db.SetStatus(id, StatusError, "下载队列已满")
	}
}

// Pause 暂停：排队中 → DB 置 paused（worker 弹到时跳过）；下载中 → cancel 传输置 paused。
func (e *FTPEngine) Pause(id int64) error {
	t, err := e.db.GetTask(id)
	if err != nil || t == nil || t.Engine != EngineFTP {
		return fmt.Errorf("任务不存在")
	}
	if t.terminal() {
		return fmt.Errorf("任务已结束，无法暂停")
	}
	e.mu.Lock()
	ctl := e.running[id]
	if ctl != nil {
		ctl.pause = true
		ctl.cancel()
	} else {
		_ = e.db.SetStatus(id, StatusPaused, "")
	}
	e.mu.Unlock()
	return nil
}

// Resume 恢复：置 pending 重新入队（worker 从本地文件大小续传）。
func (e *FTPEngine) Resume(id int64) error {
	t, err := e.db.GetTask(id)
	if err != nil || t == nil || t.Engine != EngineFTP {
		return fmt.Errorf("任务不存在")
	}
	if t.Status != StatusPaused && t.Status != StatusError {
		return fmt.Errorf("任务当前状态(%s)不可恢复", t.Status)
	}
	_ = e.db.SetStatus(id, StatusPending, "")
	e.Enqueue(id)
	return nil
}

// Remove 移除：停任务 + 删任务行。不删已下载文件（对齐云盘 remove 语义，删文件由前端单独入口）。
func (e *FTPEngine) Remove(id int64) error {
	t, err := e.db.GetTask(id)
	if err != nil || t == nil {
		return nil // 已不在库里 → 幂等成功
	}
	e.mu.Lock()
	ctl := e.running[id]
	if ctl != nil {
		ctl.remove = true
		ctl.cancel()
		e.mu.Unlock()
		return nil // 行由 worker 在 errFTPRemoved 分支删除
	}
	e.mu.Unlock()
	return e.db.DeleteTask(id)
}

// PauseAll 暂停全部 FTP 任务（engine=ftp 且非终态）。
func (e *FTPEngine) PauseAll() {
	tasks, err := e.db.ListEngineTasks(EngineFTP)
	if err != nil {
		return
	}
	for _, t := range tasks {
		if !t.terminal() {
			_ = e.Pause(t.ID)
		}
	}
}

// ResumeAll 恢复全部暂停/出错的 FTP 任务。
func (e *FTPEngine) ResumeAll() {
	tasks, err := e.db.ListEngineTasks(EngineFTP)
	if err != nil {
		return
	}
	for _, t := range tasks {
		if t.Status == StatusPaused || t.Status == StatusError {
			_ = e.Resume(t.ID)
		}
	}
}

// RemoveAll 移除全部 FTP 任务。
func (e *FTPEngine) RemoveAll() {
	tasks, err := e.db.ListEngineTasks(EngineFTP)
	if err != nil {
		return
	}
	for _, t := range tasks {
		_ = e.Remove(t.ID)
	}
}

// download 单任务传输主体（含续传/进度回写/超时/暂停响应）。t.Size/t.SavePath 会被就地更新。
func (e *FTPEngine) download(ctx context.Context, t *Task) error {
	u, err := url.Parse(t.URL)
	if err != nil {
		return fmt.Errorf("无效的 FTP 地址: %w", err)
	}
	user, pass := "anonymous", "anonymous"
	if u.User != nil {
		if v := u.User.Username(); v != "" {
			user = v
		}
		if p, ok := u.User.Password(); ok && p != "" {
			pass = p
		}
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "21")
	}

	// watchdog：ctx 取消（暂停/移除）或总超时 → 强制解阻塞所有挂起 IO
	conns := &ftpConns{}
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		totalTimer := time.NewTimer(ftpTotalTimeout)
		defer totalTimer.Stop()
		select {
		case <-ctx.Done():
		case <-totalTimer.C:
		case <-watchDone:
			return
		}
		conns.interrupt()
	}()

	opts := []ftp.DialOption{
		ftp.DialWithTimeout(ftpDialTimeout),
		ftp.DialWithShutTimeout(5 * time.Second), // Quit 等数据连接关闭状态的时限
		ftp.DialWithDialFunc(func(network, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{Timeout: ftpDialTimeout}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			conns.setCtrl(c) // 留住控制连接引用（TLS 包装也基于它，deadline 可穿透）
			return c, nil
		}),
	}
	if strings.EqualFold(u.Scheme, "ftps") {
		opts = append(opts, ftp.DialWithTLS(&tls.Config{ServerName: u.Hostname()}))
	}
	conn, err := ftp.Dial(host, opts...)
	if err != nil {
		if ctx.Err() != nil {
			return e.canceledReason(t.ID)
		}
		return fmt.Errorf("连接 FTP 服务器失败: %w", err)
	}
	defer func() { _ = conn.Quit() }()
	if err := conn.Login(user, pass); err != nil {
		if ctx.Err() != nil {
			return e.canceledReason(t.ID)
		}
		return fmt.Errorf("FTP 登录失败: %w", err)
	}

	remote := strings.TrimPrefix(u.Path, "/")
	if remote == "" {
		return fmt.Errorf("FTP 地址缺少文件路径")
	}
	size, err := conn.FileSize(remote)
	if err != nil {
		if ctx.Err() != nil {
			return e.canceledReason(t.ID)
		}
		return fmt.Errorf("查询文件大小失败: %w", err)
	}
	if size < 0 {
		size = 0
	}
	t.Size = size

	if t.Name == "" {
		t.Name = sanitizeFileName(nameFromURLPath(t.URL))
	}
	if t.Name == "" {
		t.Name = "ftp-download"
	}
	if err := os.MkdirAll(t.SaveDir, 0755); err != nil {
		return fmt.Errorf("创建保存目录失败: %w", err)
	}
	localPath := filepath.Join(t.SaveDir, t.Name)
	t.SavePath = localPath

	// 续传判定：本地已有部分文件 → 从其大小续传；超过远端大小（远端变了）→ 重来
	var offset int64
	if st, err := os.Stat(localPath); err == nil {
		offset = st.Size()
		if size > 0 {
			if offset == size {
				return nil // 已完整，直接 done
			}
			if offset > size {
				if err := os.Truncate(localPath, 0); err != nil {
					return fmt.Errorf("重置本地文件失败: %w", err)
				}
				offset = 0
			}
		}
	}
	f, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("打开本地文件失败: %w", err)
	}
	defer f.Close()

	resp, err := conn.RetrFrom(remote, uint64(offset))
	if err != nil {
		if ctx.Err() != nil {
			return e.canceledReason(t.ID)
		}
		return fmt.Errorf("开始传输失败(偏移 %d): %w", offset, err)
	}
	conns.setData(resp)
	defer func() { _ = resp.Close() }()

	_ = e.db.UpdatePoll(t.ID, StatusActive, size, offset, 0, progressOf(offset, size), "")
	start := time.Now()
	lastReport := time.Now()
	var sinceReport int64
	downloaded := offset
	buf := make([]byte, 64*1024)
	// 全局限速令牌桶
	const limitTick = 100 * time.Millisecond
	nextRefill := time.Now().Add(limitTick)
	var budget int64

	for {
		if err := ctx.Err(); err != nil {
			return e.canceledReason(t.ID)
		}
		if time.Since(start) > ftpTotalTimeout {
			return fmt.Errorf("FTP 传输总超时（%s）", ftpTotalTimeout)
		}
		// idle 超时：每次读前把数据连接 deadline 推到 60s 后，读挂死会超时报错
		_ = resp.SetDeadline(time.Now().Add(ftpIdleTimeout))
		n, rerr := resp.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fmt.Errorf("写本地文件失败: %w", werr)
			}
			downloaded += int64(n)
			sinceReport += int64(n)
			// 全局限速（令牌桶）：每 tick 最多放 limit 字节，超出则等下一 tick
			if limit := e.currentLimit(); limit > 0 {
				budget -= int64(n)
				for budget < 0 {
					wait := time.Until(nextRefill)
					if wait > 0 { select { case <-ctx.Done(): return e.canceledReason(t.ID); case <-time.After(wait): } }
					nextRefill = time.Now().Add(limitTick)
					budget += limit * int64(limitTick) / int64(time.Second)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return e.canceledReason(t.ID)
			}
			if os.IsTimeout(rerr) {
				return fmt.Errorf("传输空闲超时（%s 无数据）", ftpIdleTimeout)
			}
			return fmt.Errorf("传输中断: %w", rerr)
		}
		if time.Since(lastReport) >= ftpReportEvery {
			speed := int64(float64(sinceReport) / time.Since(lastReport).Seconds())
			_ = e.db.UpdatePoll(t.ID, StatusActive, size, downloaded, speed, progressOf(downloaded, size), "")
			lastReport, sinceReport = time.Now(), 0
		}
	}

	if size > 0 && downloaded != size {
		return fmt.Errorf("传输不完整: 已收 %d 字节，应为 %d", downloaded, size)
	}
	if size == 0 {
		t.Size = downloaded
	}
	return nil
}

// canceledReason ctx 取消后查控制柄：remove → errFTPRemoved；否则视为暂停。
func (e *FTPEngine) canceledReason(id int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c := e.running[id]; c != nil && c.remove {
		return errFTPRemoved
	}
	return errFTPPaused
}

func progressOf(done, total int64) int {
	if total <= 0 {
		return 0
	}
	return int(float64(done) / float64(total) * 100)
}

// sanitizeFileName 去掉路径分隔符等危险字符（名字来自 URL/远端，不可信）。
func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\x00", "")
	if name == "." || name == ".." {
		return "_"
	}
	return name
}

// currentLimit 全局限速（字节/秒）；settingsFn 为空或读不到返回 0（不限）。
func (e *FTPEngine) currentLimit() int64 {
	if e.settingsFn == nil { return 0 }
	return e.settingsFn()
}
