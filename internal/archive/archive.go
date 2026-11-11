// Package archive 会话上下文留档:
// 每会话保留最近 N 份完整请求体快照(客户端每次请求都携带完整历史),
// 异步单 worker 写盘, 全局容量 LRU 控制, 忙时跳过(下一请求会补全量)。
package archive

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"cluster-route/internal/config"
	"cluster-route/internal/store"
)

// Snapshot 待归档快照。
type Snapshot struct {
	Key      string
	Project  string
	Title    string
	Model    string
	MsgCount int64
	Body     []byte
	Ts       int64
}

// Service 归档服务。
type Service struct {
	mgr  *config.Manager
	st   *store.Store
	dir  string
	ch   chan Snapshot
	done chan struct{} // Run 退出后关闭

	mu      sync.Mutex
	lastSeq map[string]int64 // 会话键 -> 已分配的最大 seq(保证严格递增)
	dropped int64
	capMu   sync.Mutex // enforceCap 会被 janitor 与 ticker 同时调用
}

var safeKeyRe = regexp.MustCompile(`[^0-9a-zA-Z_-]`)

// New 创建服务; dir 为 sessions 存储根目录。
func New(mgr *config.Manager, st *store.Store, dir string) *Service {
	return &Service{
		mgr: mgr, st: st, dir: dir,
		ch:      make(chan Snapshot, 2),
		done:    make(chan struct{}),
		lastSeq: map[string]int64{},
	}
}

// Done 在 Run 退出后关闭(停机时同步用)。
func (s *Service) Done() <-chan struct{} { return s.done }

// Drain 同步处理队列中剩余快照(停机路径使用, 避免最新上下文丢失)。
func (s *Service) Drain() {
	for {
		select {
		case snap := <-s.ch:
			s.write(snap)
		default:
			return
		}
	}
}

// Submit 非阻塞投递; 满员或超大时丢弃。
func (s *Service) Submit(snap Snapshot) {
	cfg := s.mgr.Settings().Archive
	if !cfg.Enabled || snap.Key == "" || len(snap.Body) == 0 {
		return
	}
	max := int64(cfg.MaxSnapshotMB) << 20
	if max > 0 && int64(len(snap.Body)) > max {
		return
	}
	select {
	case s.ch <- snap:
	default:
		s.noteDrop()
	}
}

func (s *Service) noteDrop() {
	s.mu.Lock()
	s.dropped++
	n := s.dropped
	s.mu.Unlock()
	if n == 1 || n%50 == 0 {
		log.Printf("[archive] 警告: 归档队列已满, 已累计丢弃 %d 份快照", n)
	}
}

// Dropped 因队列满被丢弃的快照数。
func (s *Service) Dropped() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Run 后台写盘循环。
func (s *Service) Run(stop <-chan struct{}) {
	defer close(s.done)
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			s.Drain()
			return
		case snap := <-s.ch:
			s.write(snap)
		case <-ticker.C:
			s.enforceCap()
		}
	}
}

// EnforceCap 立即执行一次全局容量清理(供定期任务调用)。
func (s *Service) EnforceCap() { s.enforceCap() }

func (s *Service) write(snap Snapshot) {
	key := safeKeyRe.ReplaceAllString(snap.Key, "_")
	dir := filepath.Join(s.dir, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[archive] 创建目录失败: %v", err)
		return
	}
	// seq 必须严格递增: 列表与「每会话保留 N 份」的裁剪都按 seq DESC 进行,
	// 同一秒内的多个请求若共用 Ts 会互相覆盖文件并打乱保留顺序。
	seq := snap.Ts
	if seq == 0 {
		seq = time.Now().UnixMilli()
	}
	s.mu.Lock()
	last, seen := s.lastSeq[key]
	if !seen {
		if max, err := s.st.MaxSnapshotSeq(snap.Key); err == nil {
			last = max
		}
	}
	if seq <= last {
		seq = last + 1
	}
	s.lastSeq[key] = seq
	s.mu.Unlock()

	rel := filepath.ToSlash(filepath.Join(key, fmt.Sprintf("%d.json", seq)))
	abs := filepath.Join(s.dir, rel)
	if err := os.WriteFile(abs, snap.Body, 0o600); err != nil {
		log.Printf("[archive] 写快照失败: %v", err)
		return
	}
	row := store.SnapshotRow{
		SessionKy: snap.Key, Seq: seq, Ts: snap.Ts, Path: rel,
		SizeBytes: int64(len(snap.Body)), Model: snap.Model,
		MsgCount: snap.MsgCount, Project: snap.Project,
	}
	if _, err := s.st.InsertSnapshot(row); err != nil {
		_ = os.Remove(abs)
		log.Printf("[archive] 快照元数据写入失败: %v", err)
		return
	}
	title := snap.Title
	if title == "" {
		title = "(空会话)"
	}
	// 按 rune 截断, 避免切断 UTF-8 多字节字符产生乱码。
	if r := []rune(title); len(r) > 80 {
		title = string(r[:80])
	}
	_ = s.st.UpsertSession(store.SessionRow{
		Key: snap.Key, Project: snap.Project, Title: title,
		LastModel: snap.Model, MsgCount: snap.MsgCount,
		SizeBytes: int64(len(snap.Body)), UpdatedAt: snap.Ts,
	})
	cfg := s.mgr.Settings().Archive
	keep := cfg.KeepPerSession
	if keep <= 0 {
		keep = 3
	}
	paths, err := s.st.PruneSessionSnapshots(snap.Key, keep)
	removeFiles(s.dir, paths)
	if err != nil {
		log.Printf("[archive] 裁剪快照元数据失败: %v", err)
	}
}

// enforceCap 全局容量 LRU: 超限时从最旧快照开始删除。
func (s *Service) enforceCap() {
	s.capMu.Lock()
	defer s.capMu.Unlock()
	cfg := s.mgr.Settings().Archive
	capBytes := int64(cfg.GlobalCapGB) << 30
	if capBytes <= 0 {
		return
	}
	sum, err := s.st.SnapshotSizeSum()
	if err != nil || sum <= capBytes {
		return
	}
	for i := 0; i < 200; i++ {
		sum, err = s.st.SnapshotSizeSum()
		if err != nil || sum <= capBytes {
			return
		}
		old, err := s.st.OldestSnapshots(1)
		if err != nil || len(old) == 0 {
			return
		}
		p, err := s.st.DeleteSnapshotRow(old[0].ID)
		if err != nil {
			return
		}
		removeFiles(s.dir, []string{p})
		if err := s.st.DeleteSessionIfEmpty(old[0].SessionKy); err != nil {
			log.Printf("[archive] 清理空会话失败: %v", err)
		}
	}
}

func removeFiles(baseDir string, rels []string) {
	for _, p := range rels {
		if p == "" {
			continue
		}
		_ = os.Remove(filepath.Join(baseDir, filepath.FromSlash(p)))
	}
}

// SnapshotContent 快照文件结构化视图(返回原始 messages 供前端渲染)。
type SnapshotContent struct {
	Project  string            `json:"project"`
	Model    string            `json:"model"`
	Captured int64             `json:"captured_at"`
	MsgCount int               `json:"msg_count"`
	System   json.RawMessage   `json:"system,omitempty"`
	Messages []json.RawMessage `json:"messages"`
}

// ReadSnapshot 读取并解析快照文件。
func ReadSnapshot(absPath string) (*SnapshotContent, error) {
	fi, err := os.Stat(absPath)
	if err != nil {
		return nil, err
	}
	const maxSnapshot = 20 << 20
	if fi.Size() > maxSnapshot {
		return nil, fmt.Errorf("快照超过 %dMB", maxSnapshot>>20)
	}
	b, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return nil, fmt.Errorf("快照解析失败: %w", err)
	}
	if _, ok := probe["messages"]; !ok {
		return nil, fmt.Errorf("快照缺少 messages")
	}
	var root struct {
		System   json.RawMessage   `json:"system"`
		Messages []json.RawMessage `json:"messages"`
		Model    string            `json:"model"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("快照解析失败: %w", err)
	}
	return &SnapshotContent{
		Model: root.Model, Captured: 0,
		System: root.System, Messages: root.Messages,
		MsgCount: len(root.Messages),
	}, nil
}
