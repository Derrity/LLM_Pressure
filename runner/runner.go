package runner

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"llm_pressure/api"
)

// RunConfig 是单次固定并发压测的配置
type RunConfig struct {
	Client      *api.Client
	Req         api.ChatRequest
	Stream      bool
	Concurrency int
	Requests    int64 // >0：按总请求数停止
	Duration    time.Duration // Requests==0 时生效
	Model       string
	OnProgress  func(Progress) // 可选，周期性回调
	ProgressEvery time.Duration
}

// RunFixed 执行一次固定并发压测，返回聚合统计 + 原始样本
func RunFixed(parent context.Context, cfg RunConfig) (Stats, []Sample) {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.ProgressEvery == 0 {
		cfg.ProgressEvery = 500 * time.Millisecond
	}

	sc := stopCondition{requests: cfg.Requests, duration: cfg.Duration}
	budget, ctx, cancel := sc.budgetFor()
	defer cancel()

	// 把 parent（可能是 signal ctx）与本次 ctx 合并
	if parent != nil {
		go func() {
			select {
			case <-parent.Done():
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	// 时长型停止
	if cfg.Requests <= 0 && cfg.Duration > 0 {
		go func() {
			t := time.NewTimer(cfg.Duration)
			defer t.Stop()
			select {
			case <-t.C:
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	prog := &progress{}
	collector := newCollector()

	// 进度汇报
	var progStop chan struct{}
	if cfg.OnProgress != nil {
		progStop = make(chan struct{})
		go func() {
			t := time.NewTicker(cfg.ProgressEvery)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					cfg.OnProgress(prog.Snapshot())
					close(progStop)
					return
				case <-t.C:
					cfg.OnProgress(prog.Snapshot())
				}
			}
		}()
	}

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < cfg.Concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runWorker(ctx, cfg.Client, cfg.Req, id, cfg.Stream, budget, prog, collector)
		}(i)
	}
	wg.Wait()
	wallTime := time.Since(start)
	// workers 全部退出后，取消 ctx 让进度 goroutine 收尾
	cancel()

	if progStop != nil {
		<-progStop
	}

	samples := collector.dump()
	stats := Aggregate(samples, "fixed", cfg.Concurrency, cfg.Stream, cfg.Model, wallTime)
	return stats, samples
}

// StaircaseConfig 是阶梯扫描的配置
type StaircaseConfig struct {
	Client          *api.Client
	Req             api.ChatRequest
	Stream          bool
	Levels          []int // 并发档位，例如 {1,2,4,8,16}
	RequestsPerLevel int64 // 每档请求数（0=用 DurationPerLevel）
	DurationPerLevel time.Duration
	CoolDown        time.Duration // 档间冷却
	Model           string
	OnLevelStart    func(level, concurrency int)
	OnProgress      func(Progress)
}

// StaircaseResult 是一档的结果
type StaircaseResult struct {
	Level       int
	Concurrency int
	Stats       Stats
	Samples     []Sample
}

// RunStaircase 执行阶梯扫描，依次在每个并发档位跑一次固定压测
func RunStaircase(parent context.Context, cfg StaircaseConfig) []StaircaseResult {
	if len(cfg.Levels) == 0 {
		cfg.Levels = []int{1, 2, 4, 8, 16}
	}
	results := make([]StaircaseResult, 0, len(cfg.Levels))

	for idx, c := range cfg.Levels {
		// 检查 parent 是否已取消
		if parent != nil {
			select {
			case <-parent.Done():
				fmt.Printf("\n已中止，已完成 %d/%d 档\n", idx, len(cfg.Levels))
				return results
			default:
			}
		}
		if cfg.OnLevelStart != nil {
			cfg.OnLevelStart(idx+1, c)
		}
		subCfg := RunConfig{
			Client:        cfg.Client,
			Req:           cfg.Req,
			Stream:        cfg.Stream,
			Concurrency:   c,
			Requests:      cfg.RequestsPerLevel,
			Duration:      cfg.DurationPerLevel,
			Model:         cfg.Model,
			OnProgress:    cfg.OnProgress,
			ProgressEvery: 500 * time.Millisecond,
		}
		stats, samples := RunFixed(parent, subCfg)
		results = append(results, StaircaseResult{
			Level:       idx + 1,
			Concurrency: c,
			Stats:       stats,
			Samples:     samples,
		})
		if cfg.CoolDown > 0 && idx < len(cfg.Levels)-1 {
			fmt.Printf("  档间冷却 %s...\n", cfg.CoolDown)
			time.Sleep(cfg.CoolDown)
		}
	}
	return results
}

// InstallSignalHandler 安装 SIGINT/SIGTERM 处理：收到信号后取消 ctx 并打印提示
// 返回的 ctx 在信号到达时被取消；返回 cancel 用来主动释放
func InstallSignalHandler() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		fmt.Println("\n收到中断信号，正在结束当前批次（已采集的统计仍会输出）...")
		cancel()
	}()
	return ctx, cancel
}
