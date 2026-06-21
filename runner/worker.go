package runner

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"llm_pressure/api"
)

// progress 在运行期间被原子更新，供 UI 显示实时进度
type progress struct {
	done   atomic.Int64
	ok     atomic.Int64
	failed atomic.Int64
	tokens atomic.Int64
}

// Progress 是 progress 的只读快照
type Progress struct {
	Done   int64
	OK     int64
	Failed int64
	Tokens int64
}

// Snapshot 读取当前进度
func (p *progress) Snapshot() Progress {
	return Progress{
		Done:   p.done.Load(),
		OK:     p.ok.Load(),
		Failed: p.failed.Load(),
		Tokens: p.tokens.Load(),
	}
}

// workerFunc 是单个 worker 的执行循环
// budget 是剩余请求数（用原子计数实现）；每发一个请求就 decrement
// 当 budget <= 0 或 ctx 取消时退出
func runWorker(
	ctx context.Context,
	client *api.Client,
	req api.ChatRequest,
	workerID int,
	stream bool,
	budget *atomic.Int64,
	prog *progress,
	collector *sampleCollector,
) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// 申请一个请求名额
		remaining := budget.Add(-1)
		if remaining < 0 {
			// 把多扣的那个还回去（虽然即将退出，但保持计数准确）
			budget.Add(1)
			return
		}

		var r api.Result
		if stream {
			r = client.ChatStream(ctx, req)
		} else {
			r = client.Chat(ctx, req)
		}

		sm := Sample{
			WorkerID:         workerID,
			Stream:           stream,
			Success:          r.Err == nil,
			HTTPStatus:       r.HTTPStatus,
			ErrMsg:           errString(r.Err),
			TotalLatency:     r.TotalLatency,
			TTFT:             r.TTFT,
			GenerationTime:   r.GenerationTime,
			PromptTokens:     r.PromptTokens,
			CompletionTokens: r.CompletionTokens,
			TotalTokens:      r.TotalTokens,
			TokenSource:      r.TokenSource,
		}
		collector.add(sm)

		prog.done.Add(1)
		if sm.Success {
			prog.ok.Add(1)
			prog.tokens.Add(int64(sm.CompletionTokens))
		} else {
			prog.failed.Add(1)
		}
	}
}

// sampleCollector 线程安全地收集样本
type sampleCollector struct {
	mu      sync.Mutex
	samples []Sample
}

func newCollector() *sampleCollector { return &sampleCollector{} }

func (c *sampleCollector) add(s Sample) {
	c.mu.Lock()
	c.samples = append(c.samples, s)
	c.mu.Unlock()
}

func (c *sampleCollector) dump() []Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Sample, len(c.samples))
	copy(out, c.samples)
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// stopCondition 封装 worker 请求预算；当前 CLI 只暴露按请求数停止。
type stopCondition struct {
	requests int64
	duration time.Duration
}

// budgetFor 返回初始 budget（用 *atomic.Int64 实现）。
func (sc stopCondition) budgetFor() (*atomic.Int64, context.Context, context.CancelFunc) {
	budget := &atomic.Int64{}
	if sc.requests > 0 {
		budget.Store(sc.requests)
	} else {
		budget.Store(1 << 62) // 近似无限
	}
	ctx, cancel := context.WithCancel(context.Background())
	return budget, ctx, cancel
}
