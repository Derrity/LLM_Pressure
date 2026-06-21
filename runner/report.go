package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 保存到磁盘的报告结构
type Report struct {
	Timestamp string            `json:"timestamp"`
	Model     string            `json:"model"`
	Stream    bool              `json:"stream"`
	Mode      string            `json:"mode"`
	Request   ReportRequest     `json:"request"`
	Fixed     *Stats            `json:"fixed,omitempty"`
	Staircase []StaircaseResult `json:"staircase,omitempty"`
}

// ReportRequest 记录本次压测使用的内置请求参数。
type ReportRequest struct {
	Prompt      string  `json:"prompt,omitempty"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
	Temperature float64 `json:"temperature"`
}

// PrintFixed 打印单次固定并发压测的统计
func PrintFixed(s Stats) {
	fmt.Println(divider('═'))
	fmt.Printf("  模型: %s   |  模式: %s  %s  |  并发: %d\n",
		s.Model, modeLabel(s.Mode), streamLabel(s.Stream), s.Concurrency)
	fmt.Printf("  总耗时: %s   |  请求: %d (成功 %d / 失败 %d)\n",
		roundDur(s.WallTime), s.Total, s.Success, s.Failed)
	fmt.Printf("  完成 token: %d   |  prompt token: %d   |  总吞吐: %.2f tokens/s\n",
		s.CompletionTokens, s.PromptTokens, s.TotalThroughput)
	if s.EstimatedTokens {
		fmt.Println("  ⚠ 上游未返回 usage，token 数为估算值（按空格分词，中文偏低）")
	}
	fmt.Println(divider('─'))

	// 延迟分布表
	fmt.Println("  延迟分布:")
	printLatencyRow("  端到端", s.TotalLatency, false)
	if s.Stream {
		printLatencyRow("  首 token (TTFT)", s.TTFTStat, false)
		printLatencyRow("  生成耗时", s.GenTimeStat, false)
	}
	printLatencyRow("  单请求 TPS", s.ReqTPSStat, true)
	fmt.Println(divider('─'))

	// 每线程
	if len(s.Workers) > 0 {
		fmt.Println("  每线程吞吐:")
		fmt.Printf("    %-10s %10s %10s %12s %14s\n", "worker", "请求数", "成功", "完成token", "TPS")
		for _, w := range s.Workers {
			fmt.Printf("    %-10d %10d %10d %12d %14.2f\n",
				w.WorkerID, w.Count, w.Success, w.CompletionTokens, w.TPS)
		}
		// 汇总行
		fmt.Printf("    %-10s %10d %10d %12d %14.2f\n",
			"SUM/avg", sumWorkerCount(s.Workers), sumWorkerOK(s.Workers),
			sumWorkerTokens(s.Workers), avgWorkerTPS(s.Workers))
	}
	fmt.Println(divider('─'))

	// 错误分布
	if len(s.ErrorDist) > 0 {
		fmt.Println("  错误分布:")
		items := sortedErrors(s.ErrorDist)
		const maxErrors = 8
		for i, item := range items {
			if i >= maxErrors {
				remaining := 0
				for _, rest := range items[i:] {
					remaining += rest.Count
				}
				fmt.Printf("    ... 另有 %d 条错误样本，详见 JSON 报告\n", remaining)
				break
			}
			msg, n := item.Message, item.Count
			short := msg
			if len(short) > 180 {
				short = short[:177] + "..."
			}
			fmt.Printf("    [%d] %s\n", n, short)
		}
		fmt.Println(divider('─'))
	}
}

// PrintStaircase 打印阶梯扫描的对比表
func PrintStaircase(results []StaircaseResult, stream bool) {
	fmt.Println(divider('═'))
	fmt.Printf("  阶梯扫描汇总  |  %s  |  模型: %s\n", streamLabel(stream), firstModel(results))
	fmt.Println(divider('─'))
	fmt.Printf("  %-6s %-8s %-12s %-14s %-14s %-14s %-10s\n",
		"档", "并发", "总耗时", "总吞吐tps", "p95端到端", "p95 TTFT", "失败率")
	for _, r := range results {
		s := r.Stats
		failedRate := 0.0
		if s.Total > 0 {
			failedRate = float64(s.Failed) / float64(s.Total) * 100
		}
		ttft := "-"
		if stream && s.TTFTStat.Count > 0 {
			ttft = roundDur(s.TTFTStat.P95).String()
		}
		fmt.Printf("  %-6d %-8d %-12s %-14.2f %-14s %-14s %-10s\n",
			r.Level, r.Concurrency, roundDur(s.WallTime),
			s.TotalThroughput, roundDur(s.TotalLatency.P95), ttft,
			fmt.Sprintf("%.1f%%", failedRate))
	}
	fmt.Println(divider('─'))
	fmt.Println("  各档详细:")
	for _, r := range results {
		fmt.Printf("\n[档 %d / 并发 %d]\n", r.Level, r.Concurrency)
		PrintFixed(r.Stats)
	}
}

// SaveReport 把报告写入 results/{timestamp}_{mode}.json，返回路径
func SaveReport(rep Report) (string, error) {
	if rep.Timestamp == "" {
		rep.Timestamp = time.Now().Format("20060102_150405")
	}
	dir := "results"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s_%s_%s.json", rep.Timestamp, rep.Mode, reportStreamName(rep.Stream))
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		return "", err
	}
	return path, nil
}

// ---- helpers ----

type errorItem struct {
	Message string
	Count   int
}

func sortedErrors(dist map[string]int) []errorItem {
	items := make([]errorItem, 0, len(dist))
	for msg, count := range dist {
		items = append(items, errorItem{Message: msg, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Message < items[j].Message
		}
		return items[i].Count > items[j].Count
	})
	return items
}

func reportStreamName(stream bool) string {
	if stream {
		return "stream"
	}
	return "nonstream"
}

func printLatencyRow(label string, ls LatencyStat, isTPS bool) {
	if ls.Count == 0 {
		fmt.Printf("%s: 无数据\n", label)
		return
	}
	if isTPS {
		fmt.Printf("%-18s n=%d  min=%.2f  p50=%.2f  p95=%.2f  p99=%.2f  max=%.2f  mean=%.2f  tokens/s\n",
			label, ls.Count,
			ls.Min.Seconds(), ls.P50.Seconds(), ls.P95.Seconds(),
			ls.P99.Seconds(), ls.Max.Seconds(), ls.Mean.Seconds())
	} else {
		fmt.Printf("%-18s n=%d  min=%s  p50=%s  p95=%s  p99=%s  max=%s  mean=%s\n",
			label, ls.Count,
			roundDur(ls.Min), roundDur(ls.P50), roundDur(ls.P95),
			roundDur(ls.P99), roundDur(ls.Max), roundDur(ls.Mean))
	}
}

func roundDur(d time.Duration) time.Duration {
	if d < 0 {
		return d
	}
	switch {
	case d < time.Microsecond:
		return d.Round(time.Nanosecond)
	case d < time.Millisecond:
		return d.Round(time.Microsecond)
	default:
		return d.Round(time.Millisecond)
	}
}

func modeLabel(m string) string {
	switch m {
	case "fixed":
		return "固定并发"
	case "staircase":
		return "阶梯扫描"
	}
	return m
}

func streamLabel(s bool) string {
	if s {
		return "[流式]"
	}
	return "[非流式]"
}

func divider(r rune) string {
	return "  " + strings.Repeat(string(r), 70)
}

func firstModel(rs []StaircaseResult) string {
	for _, r := range rs {
		if r.Stats.Model != "" {
			return r.Stats.Model
		}
	}
	return "?"
}

func sumWorkerCount(ws []WorkerStat) int {
	n := 0
	for _, w := range ws {
		n += w.Count
	}
	return n
}
func sumWorkerOK(ws []WorkerStat) int {
	n := 0
	for _, w := range ws {
		n += w.Success
	}
	return n
}
func sumWorkerTokens(ws []WorkerStat) int {
	n := 0
	for _, w := range ws {
		n += w.CompletionTokens
	}
	return n
}
func avgWorkerTPS(ws []WorkerStat) float64 {
	if len(ws) == 0 {
		return 0
	}
	// 总吞吐 = 总 token / 总工作时间
	var tokens int
	var busy time.Duration
	for _, w := range ws {
		tokens += w.CompletionTokens
		busy += w.WallTime
	}
	if busy <= 0 {
		return 0
	}
	return float64(tokens) / busy.Seconds()
}
