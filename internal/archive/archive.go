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
	mgr *config.Manager
	st  *store.Store
	dir string
	ch  chan Snapshot
}

var safeKeyRe = regexp.MustCompile(`[^0-9a-zA-Z_-]`)

// New 创建服务; dir 为 sessions 存储根目录。
func New(mgr *config.Manager, st *store.Store, dir string) *Service {
	return &Service{mgr: mgr, st: st, dir: dir, ch: make(chan Snapshot, 2)}
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
	}
}

// Run 后台写盘循环。
func (s *Service) Run(stop <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
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
	seq := snap.Ts
	if seq == 0 {
		seq = time.Now().UnixMilli()
	}
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
		log.Printf("[archive] 快照元数据写入失败: %v", err)
		return
	}
	title := snap.Title
	if title == "" {
		title = "(空会话)"
	}
	if len(title) > 80 {
		title = title[:80]
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
	if paths, err := s.st.PruneSessionSnapshots(snap.Key, keep); err == nil {
		removeFiles(s.dir, paths)
	}
}

// enforceCap 全局容量 LRU: 超限时从最旧快照开始删除。
func (s *Service) enforceCap() {
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
	b, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
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
