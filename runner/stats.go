// Package runner 实现压测执行、采样与统计聚合。
package runner

import (
	"sort"
	"time"
)

// Sample 是一次请求的采样数据
type Sample struct {
	WorkerID        int           `json:"worker_id"`
	Stream          bool          `json:"stream"`
	Success         bool          `json:"success"`
	HTTPStatus      int           `json:"http_status,omitempty"`
	ErrMsg          string        `json:"err,omitempty"`
	TotalLatency    time.Duration `json:"total_latency_ns"`
	TTFT            time.Duration `json:"ttft_ns,omitempty"`
	GenerationTime  time.Duration `json:"generation_time_ns"`
	PromptTokens    int           `json:"prompt_tokens"`
	CompletionTokens int          `json:"completion_tokens"`
	TotalTokens     int           `json:"total_tokens"`
	TokenSource     string        `json:"token_source,omitempty"`
}

// LatencyStat 是一组延迟/耗时的分布统计
type LatencyStat struct {
	Count int           `json:"count"`
	Min   time.Duration `json:"min_ns"`
	P50   time.Duration `json:"p50_ns"`
	P95   time.Duration `json:"p95_ns"`
	P99   time.Duration `json:"p99_ns"`
	Max   time.Duration `json:"max_ns"`
	Mean  time.Duration `json:"mean_ns"`
}

// WorkerStat 是单个 worker 的统计
type WorkerStat struct {
	WorkerID        int           `json:"worker_id"`
	Count           int           `json:"count"`
	Success         int           `json:"success"`
	Failed          int           `json:"failed"`
	CompletionTokens int          `json:"completion_tokens"`
	WallTime        time.Duration `json:"wall_time_ns"`
	TPS             float64       `json:"tps"` // 该 worker 在其活跃时段内的 tokens/s
}

// Stats 是一组采样聚合后的统计结果
type Stats struct {
	Mode         string        `json:"mode"`
	Concurrency  int           `json:"concurrency"`
	Stream       bool          `json:"stream"`
	Model        string        `json:"model"`
	WallTime     time.Duration `json:"wall_time_ns"`
	Total        int           `json:"total"`
	Success      int           `json:"success"`
	Failed       int           `json:"failed"`
	CompletionTokens int       `json:"completion_tokens"`
	PromptTokens int           `json:"prompt_tokens"`
	TotalThroughput float64    `json:"total_throughput_tps"` // 总吞吐 tokens/s

	TotalLatency LatencyStat `json:"total_latency"`
	TTFTStat     LatencyStat `json:"ttft"`
	GenTimeStat  LatencyStat `json:"generation_time"`
	ReqTPSStat   LatencyStat `json:"req_tps"` // 单请求 TPS 分布

	Workers        []WorkerStat        `json:"workers"`
	ErrorDist      map[string]int      `json:"error_dist,omitempty"`
	EstimatedTokens bool               `json:"estimated_tokens"`
}

// Aggregate 把一批 Sample 聚合成 Stats
func Aggregate(samples []Sample, mode string, concurrency int, stream bool, model string, wallTime time.Duration) Stats {
	s := Stats{
		Mode:        mode,
		Concurrency: concurrency,
		Stream:      stream,
		Model:       model,
		WallTime:    wallTime,
		Total:       len(samples),
		ErrorDist:   map[string]int{},
	}

	workerAgg := map[int]*WorkerStat{}

	var totalLatencies []time.Duration
	var ttfts []time.Duration
	var genTimes []time.Duration
	var reqTPS []float64

	for _, sm := range samples {
		if sm.Success {
			s.Success++
			s.CompletionTokens += sm.CompletionTokens
			s.PromptTokens += sm.PromptTokens
			if sm.TokenSource == "estimate" {
				s.EstimatedTokens = true
			}
			totalLatencies = append(totalLatencies, sm.TotalLatency)
			if sm.Stream && sm.TTFT > 0 {
				ttfts = append(ttfts, sm.TTFT)
			}
			if sm.GenerationTime > 0 {
				genTimes = append(genTimes, sm.GenerationTime)
				reqTPS = append(reqTPS, float64(sm.CompletionTokens)/sm.GenerationTime.Seconds())
			}
		} else {
			s.Failed++
			key := sm.ErrMsg
			if key == "" {
				key = "unknown"
			}
			s.ErrorDist[key]++
		}

		ws, ok := workerAgg[sm.WorkerID]
		if !ok {
			ws = &WorkerStat{WorkerID: sm.WorkerID}
			workerAgg[sm.WorkerID] = ws
		}
		ws.Count++
		if sm.Success {
			ws.Success++
			ws.CompletionTokens += sm.CompletionTokens
		} else {
			ws.Failed++
		}
	}

	s.TotalLatency = latencyStatOf(totalLatencies)
	s.TTFTStat = latencyStatOf(ttfts)
	s.GenTimeStat = latencyStatOf(genTimes)
	s.ReqTPSStat = tpsStatOf(reqTPS)

	if wallTime > 0 {
		s.TotalThroughput = float64(s.CompletionTokens) / wallTime.Seconds()
	}

	// per-worker
	ids := make([]int, 0, len(workerAgg))
	for id := range workerAgg {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		ws := workerAgg[id]
		// worker 的活跃时长用其所有请求的总延迟和近似（串行发请求）
		// 注意：这是 worker 实际工作时间，不含等待分配的空隙
		var workerBusy time.Duration
		for _, sm := range samples {
			if sm.WorkerID == id && sm.Success {
				workerBusy += sm.TotalLatency
			}
		}
		ws.WallTime = workerBusy
		if workerBusy > 0 {
			ws.TPS = float64(ws.CompletionTokens) / workerBusy.Seconds()
		}
		s.Workers = append(s.Workers, *ws)
	}

	return s
}

func latencyStatOf(ds []time.Duration) LatencyStat {
	if len(ds) == 0 {
		return LatencyStat{}
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	return LatencyStat{
		Count: len(sorted),
		Min:   sorted[0],
		P50:   percentile(sorted, 0.50),
		P95:   percentile(sorted, 0.95),
		P99:   percentile(sorted, 0.99),
		Max:   sorted[len(sorted)-1],
		Mean:  sum / time.Duration(len(sorted)),
	}
}

// tpsStatOf 复用 LatencyStat 结构，单位是 tokens/s（存为 ns 只是为了复用结构，
// 报告输出时换算）。这里我们直接把 tps 当作 float64 写入 LatencyStat 的字段意义不通，
// 因此单独构造一个 "TPS 分布" 并以 ns 形式存储 tps * 1e9，输出时除回去。
func tpsStatOf(tps []float64) LatencyStat {
	if len(tps) == 0 {
		return LatencyStat{}
	}
	sorted := append([]float64(nil), tps...)
	sort.Float64s(sorted)
	toDur := func(v float64) time.Duration { return time.Duration(v * 1e9) } // ns 形式存 tps
	var sum float64
	for _, v := range sorted {
		sum += v
	}
	mean := sum / float64(len(sorted))
	return LatencyStat{
		Count: len(sorted),
		Min:   toDur(sorted[0]),
		P50:   toDur(percentileF(sorted, 0.50)),
		P95:   toDur(percentileF(sorted, 0.95)),
		P99:   toDur(percentileF(sorted, 0.99)),
		Max:   toDur(sorted[len(sorted)-1]),
		Mean:  toDur(mean),
	}
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

func percentileF(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}
