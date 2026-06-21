package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"llm_pressure/term"
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
	printRunHeader(runTitle(s), s)
	printResultBlock(s)
	printLatencyBlock(s)
	printWorkersBlock(s)
	printErrorsBlock(s)
}

// PrintComparison 打印固定并发下多个请求模式的对比表。
func PrintComparison(stats []Stats) {
	if len(stats) < 2 {
		return
	}
	fmt.Println()
	printBoxTitle("Comparison")
	fmt.Printf("  %-12s %-10s %-6s %-10s %-13s %-12s %-12s %-13s %-10s\n",
		"mode", "ok/total", "fail", "tokens", "throughput", "avg thread", "best thread", "p95 latency", "p95 TTFT")
	for _, s := range stats {
		ttft := "-"
		if s.Stream && s.TTFTStat.Count > 0 {
			ttft = dur(s.TTFTStat.P95)
		}
		mode := pad(modeName(s.Stream), 12)
		if s.Failed > 0 {
			mode = term.Yellow(mode)
		} else {
			mode = term.Green(mode)
		}
		fmt.Printf("  %s %-10s %-6d %-10s %-13s %-12s %-12s %-13s %-10s\n",
			mode,
			fmt.Sprintf("%d/%d", s.Success, s.Total),
			s.Failed,
			intFmt(s.CompletionTokens),
			fmt.Sprintf("%.2f/s", s.TotalThroughput),
			fmt.Sprintf("%.2f/s", s.AvgThreadTPS),
			fmt.Sprintf("%.2f/s", s.BestThreadTPS),
			dur(s.TotalLatency.P95),
			ttft)
	}
	printVerdict(stats)
}

// PrintModelSummary 打印多模型固定并发测试的最终汇总。
func PrintModelSummary(stats []Stats) {
	if len(stats) <= 1 {
		return
	}
	fmt.Println()
	printBoxTitle("Model Summary")
	fmt.Printf("  %-28s %-12s %-10s %-6s %-10s %-13s %-12s %-12s %-13s %-10s\n",
		"model", "mode", "ok/total", "fail", "tokens", "throughput", "avg thread", "best thread", "p95 latency", "p95 TTFT")
	for _, s := range stats {
		ttft := "-"
		if s.Stream && s.TTFTStat.Count > 0 {
			ttft = dur(s.TTFTStat.P95)
		}
		mode := pad(modeName(s.Stream), 12)
		if s.Failed > 0 {
			mode = term.Yellow(mode)
		} else {
			mode = term.Green(mode)
		}
		fmt.Printf("  %-28s %s %-10s %-6d %-10s %-13s %-12s %-12s %-13s %-10s\n",
			trunc(s.Model, 28),
			mode,
			fmt.Sprintf("%d/%d", s.Success, s.Total),
			s.Failed,
			intFmt(s.CompletionTokens),
			fmt.Sprintf("%.2f/s", s.TotalThroughput),
			fmt.Sprintf("%.2f/s", s.AvgThreadTPS),
			fmt.Sprintf("%.2f/s", s.BestThreadTPS),
			dur(s.TotalLatency.P95),
			ttft)
	}
	printBestThroughput(stats)
}

// PrintStaircaseModelSummary 打印多模型阶梯扫描的最终汇总。
func PrintStaircaseModelSummary(results []StaircaseResult) {
	if len(results) <= 1 {
		return
	}
	fmt.Println()
	printBoxTitle("Model Staircase Summary")
	fmt.Printf("  %-28s %-12s %-6s %-8s %-10s %-13s %-12s %-12s %-13s %-10s\n",
		"model", "mode", "level", "threads", "ok/total", "throughput", "avg thread", "best thread", "p95 latency", "fail")
	for _, r := range results {
		s := r.Stats
		failRate := 0.0
		if s.Total > 0 {
			failRate = float64(s.Failed) / float64(s.Total) * 100
		}
		mode := pad(modeName(s.Stream), 12)
		if s.Failed > 0 {
			mode = term.Yellow(mode)
		} else {
			mode = term.Green(mode)
		}
		fmt.Printf("  %-28s %s %-6d %-8d %-10s %-13s %-12s %-12s %-13s %-10s\n",
			trunc(s.Model, 28),
			mode,
			r.Level,
			r.Concurrency,
			fmt.Sprintf("%d/%d", s.Success, s.Total),
			fmt.Sprintf("%.2f/s", s.TotalThroughput),
			fmt.Sprintf("%.2f/s", s.AvgThreadTPS),
			fmt.Sprintf("%.2f/s", s.BestThreadTPS),
			dur(s.TotalLatency.P95),
			fmt.Sprintf("%.1f%%", failRate))
	}
}

func printRunHeader(title string, s Stats) {
	fmt.Println()
	printBoxTitle(title)
	fmt.Printf("  %s  %s   %s   %s   %s\n",
		term.Cyan("done"),
		fmt.Sprintf("%d/%d", s.Total, s.Total),
		statusText(s),
		fmt.Sprintf("%s tok", intFmt(s.CompletionTokens)),
		fmt.Sprintf("elapsed %s", dur(s.WallTime)))
}

func printResultBlock(s Stats) {
	fmt.Println()
	fmt.Println(term.Cyan("Result"))
	fmt.Printf("  %-12s %s\n", "status", statusLabel(s))
	fmt.Printf("  %-12s %s\n", "success", successRateLabel(s))
	fmt.Printf("  %-12s %s\n", "wall time", dur(s.WallTime))
	fmt.Printf("  %-12s %s\n", "throughput", term.Bold(fmt.Sprintf("%.2f tok/s", s.TotalThroughput)))
	fmt.Printf("  %-12s %s\n", "avg thread", fmt.Sprintf("%.2f tok/s", s.AvgThreadTPS))
	fmt.Printf("  %-12s %s\n", "best thread", fmt.Sprintf("%.2f tok/s", s.BestThreadTPS))
	fmt.Printf("  %-12s %s\n", "tokens", intFmt(s.CompletionTokens))
	if s.PromptTokens > 0 {
		fmt.Printf("  %-12s %s\n", "prompt tok", intFmt(s.PromptTokens))
	}
	if s.EstimatedTokens {
		fmt.Printf("  %s\n", term.Yellow("token usage estimated; upstream did not return usage"))
	}
}

func printLatencyBlock(s Stats) {
	fmt.Println()
	fmt.Println(term.Cyan("Latency"))
	printLatencyLine("end-to-end", s.TotalLatency, false)
	if s.Stream {
		printLatencyLine("TTFT", s.TTFTStat, false)
		printLatencyLine("generation", s.GenTimeStat, false)
	}
	printLatencyLine("req TPS", s.ReqTPSStat, true)
}

func printWorkersBlock(s Stats) {
	if len(s.Workers) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(term.Cyan("Workers"))
	fmt.Printf("  %-5s %5s %5s %6s %9s %9s\n", "id", "req", "ok", "fail", "tokens", "avg TPS")
	for _, w := range s.Workers {
		fail := fmt.Sprintf("%d", w.Failed)
		if w.Failed > 0 {
			fail = term.Red(fail)
		}
		fmt.Printf("  %-5d %5d %5d %6s %9s %9.2f\n",
			w.WorkerID, w.Count, w.Success, fail, intFmt(w.CompletionTokens), w.TPS)
	}
	fmt.Printf("  %s\n", term.Gray(strings.Repeat("─", 46)))
	fail := fmt.Sprintf("%d", sumWorkerFail(s.Workers))
	if s.Failed > 0 {
		fail = term.Red(fail)
	}
	fmt.Printf("  %-5s %5d %5d %6s %9s %9.2f\n",
		"all", sumWorkerCount(s.Workers), sumWorkerOK(s.Workers),
		fail, intFmt(sumWorkerTokens(s.Workers)), s.AvgThreadTPS)
}

func printErrorsBlock(s Stats) {
	if len(s.ErrorDist) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(term.Red("Errors"))
	items := sortedErrors(s.ErrorDist)
	const maxErrors = 6
	for i, item := range items {
		if i >= maxErrors {
			remaining := 0
			for _, rest := range items[i:] {
				remaining += rest.Count
			}
			fmt.Printf("  ... %d more failed samples in JSON report\n", remaining)
			break
		}
		fmt.Printf("  %dx %s\n", item.Count, compact(item.Message, 140))
	}
}

// PrintStaircase 打印阶梯扫描的对比表
func PrintStaircase(results []StaircaseResult, stream bool) {
	fmt.Println()
	printBoxTitle(fmt.Sprintf("Staircase %s", modeName(stream)))
	fmt.Printf("  %-6s %-8s %-12s %-14s %-14s %-14s %-10s\n",
		"level", "threads", "elapsed", "throughput", "p95 latency", "p95 TTFT", "fail")
	for _, r := range results {
		s := r.Stats
		failedRate := 0.0
		if s.Total > 0 {
			failedRate = float64(s.Failed) / float64(s.Total) * 100
		}
		ttft := "-"
		if stream && s.TTFTStat.Count > 0 {
			ttft = dur(s.TTFTStat.P95)
		}
		fmt.Printf("  %-6d %-8d %-12s %-14.2f %-14s %-14s %-10s\n",
			r.Level, r.Concurrency, dur(s.WallTime),
			s.TotalThroughput, dur(s.TotalLatency.P95), ttft,
			fmt.Sprintf("%.1f%%", failedRate))
	}
	fmt.Println()
	fmt.Println(term.Cyan("Details"))
	for _, r := range results {
		fmt.Printf("\n%s\n", term.Gray(fmt.Sprintf("level %d / concurrency %d", r.Level, r.Concurrency)))
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
	name := fmt.Sprintf("%s_%s_%s_%s.json", rep.Timestamp, reportModelName(rep.Model), rep.Mode, reportStreamName(rep.Stream))
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

func reportModelName(model string) string {
	if model == "" {
		return "model"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(model) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "model"
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

func printLatencyLine(label string, ls LatencyStat, isTPS bool) {
	if ls.Count == 0 {
		fmt.Printf("  %-12s %s\n", label, term.Gray("no data"))
		return
	}
	if isTPS {
		fmt.Printf("  %-12s p50 %-8.2f p95 %-8.2f max %-8.2f\n",
			label, ls.P50.Seconds(), ls.P95.Seconds(), ls.Max.Seconds())
		return
	}
	fmt.Printf("  %-12s p50 %-9s p95 %-9s max %-9s\n",
		label, dur(ls.P50), dur(ls.P95), dur(ls.Max))
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

func modeName(stream bool) string {
	if stream {
		return "stream"
	}
	return "non-stream"
}

func runTitle(s Stats) string {
	return strings.Title(modeName(s.Stream))
}

func printBoxTitle(title string) {
	width := 52
	if len(title)+4 > width {
		width = len(title) + 4
	}
	fmt.Printf("╭─ %s %s╮\n", term.Cyan(title), term.Gray(strings.Repeat("─", width-len(title)-4)))
	fmt.Printf("╰%s╯\n", term.Gray(strings.Repeat("─", width)))
}

func statusText(s Stats) string {
	ok := term.Green(fmt.Sprintf("%d ok", s.Success))
	failed := fmt.Sprintf("%d failed", s.Failed)
	if s.Failed > 0 {
		failed = term.Red(failed)
	}
	return ok + "   " + failed
}

func statusLabel(s Stats) string {
	switch {
	case s.Total == 0:
		return term.Gray("no data")
	case s.Failed == 0:
		return term.Green("success")
	case s.Success > 0:
		return term.Yellow("partial success")
	default:
		return term.Red("failed")
	}
}

func successRateLabel(s Stats) string {
	rate := successRate(s)
	text := fmt.Sprintf("%.1f%%", rate)
	switch {
	case s.Total == 0:
		return term.Gray("0.0%")
	case rate >= 99.9:
		return term.Green(text)
	case rate >= 90:
		return term.Yellow(text)
	default:
		return term.Red(text)
	}
}

func successRate(s Stats) float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Success) / float64(s.Total) * 100
}

func printVerdict(stats []Stats) {
	fmt.Println()
	fmt.Println(term.Cyan("Verdict"))
	for _, s := range stats {
		label := pad(modeName(s.Stream), 11)
		if s.Failed == 0 {
			fmt.Printf("  %s %s\n", term.Green(label), "stable, no failures")
			continue
		}
		fmt.Printf("  %s %s\n", term.Yellow(label), fmt.Sprintf("%d provider/client failures", s.Failed))
	}
}

func printBestThroughput(stats []Stats) {
	var best *Stats
	for i := range stats {
		s := &stats[i]
		if s.Success == 0 {
			continue
		}
		if best == nil || s.TotalThroughput > best.TotalThroughput {
			best = s
		}
	}
	if best == nil {
		return
	}
	fmt.Println()
	fmt.Println(term.Cyan("Best"))
	fmt.Printf("  throughput  %s  %s  %.2f tok/s\n", best.Model, modeName(best.Stream), best.TotalThroughput)
}

func dur(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	return roundDur(d).String()
}

func intFmt(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	prefix := len(s) % 3
	if prefix == 0 {
		prefix = 3
	}
	b.WriteString(s[:prefix])
	for i := prefix; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func compact(s string, maxLen int) string {
	s = strings.Join(strings.Fields(s), " ")
	if maxLen > 0 && len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}

func trunc(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
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
func sumWorkerFail(ws []WorkerStat) int {
	n := 0
	for _, w := range ws {
		n += w.Failed
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
