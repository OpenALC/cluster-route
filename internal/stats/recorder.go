// Package stats 提供非阻塞统计采集: 请求路径只投递有界队列,
// 后台协程按批落库并计算成本, 保证转发零阻塞。
package stats

import (
	"log"
	"sync"
	"time"

	"cluster-route/internal/pricing"
	"cluster-route/internal/store"
)

// Recorder 异步统计记录器。
type Recorder struct {
	ch      chan store.Record
	dropped chan struct{} // 容量信号, 非阻塞丢弃计数
	store   *store.Store
	pricer  *pricing.Pricer
	done    chan struct{} // Run 退出(含最后一次落盘)后关闭

	mu        sync.Mutex
	dropCount int64
	batch     []store.Record
}

// New 创建记录器; queueCap 为队列容量。
func New(st *store.Store, pr *pricing.Pricer, queueCap int) *Recorder {
	if queueCap <= 0 {
		queueCap = 10000
	}
	return &Recorder{
		ch:      make(chan store.Record, queueCap),
		dropped: make(chan struct{}, queueCap),
		store:   st,
		pricer:  pr,
		done:    make(chan struct{}),
	}
}

// Done 在 Run 完成最后一次落盘后关闭; 停机时先用它同步, 再关数据库。
func (r *Recorder) Done() <-chan struct{} { return r.done }

// Submit 非阻塞投递; 队列满时丢弃(本地统计可容忍, 避免拖慢转发)。
func (r *Recorder) Submit(rec store.Record) {
	select {
	case r.ch <- rec:
	default:
		select {
		case r.dropped <- struct{}{}:
		default:
		}
	}
}

// Run 后台批量落库循环: 每 500 条或每秒 flush 一次。
func (r *Recorder) Run(stop <-chan struct{}) {
	defer close(r.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			// 先把队列中尚未消费的记录取出, 再落盘: 否则停机瞬间仍在
			// channel 内的记录会被静默丢弃(转发已经返回, 用户看不到任何提示)。
			r.drain()
			r.flush()
			return
		case rec := <-r.ch:
			r.push(rec)
			if len(r.batch) >= 500 {
				r.flush()
			}
		case <-ticker.C:
			r.flush()
		case <-r.dropped:
			r.mu.Lock()
			r.dropCount++
			if r.dropCount%500 == 1 {
				log.Printf("[stats] 警告: 队列已满, 已累计丢弃 %d 条统计", r.dropCount)
			}
			r.mu.Unlock()
		}
	}
}

func (r *Recorder) push(rec store.Record) {
	rec.Cost = r.pricer.Cost(rec.Upstream, time.Unix(rec.Ts, 0), rec.Input, rec.CacheRead, rec.CacheCreate, rec.Output)
	r.mu.Lock()
	r.batch = append(r.batch, rec)
	r.mu.Unlock()
}

// drain 取出 channel 中剩余的记录(停机路径使用)。
func (r *Recorder) drain() {
	for {
		select {
		case rec := <-r.ch:
			r.push(rec)
		default:
			return
		}
	}
}

func (r *Recorder) flush() {
	r.mu.Lock()
	b := r.batch
	r.batch = nil
	r.mu.Unlock()
	if len(b) == 0 {
		return
	}
	if err := r.store.InsertBatch(b); err != nil {
		log.Printf("[stats] 落库失败(丢弃 %d 条): %v", len(b), err)
	}
}

// Dropped 已丢弃条数(管理接口展示)。
func (r *Recorder) Dropped() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropCount
}

// Flush 同步落盘当前批次(含通道中已投递但未被 Run 消费的记录)。
func (r *Recorder) Flush() {
	r.drain()
	r.flush()
}
