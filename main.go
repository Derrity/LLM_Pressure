package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"llm_pressure/api"
	"llm_pressure/config"
	"llm_pressure/runner"
	"llm_pressure/term"
	"llm_pressure/ui"
)

const (
	defaultPrompt = "Write a continuous original story in English about a developer load-testing a new language model at midnight. Keep it around 350 words, use plain paragraphs, and do not use markdown or bullet lists."

	defaultRequests    = 50
	defaultMaxTokens   = 512
	defaultTemperature = 0.0
	probeMaxTokens     = 64
	probeTimeout       = 90 * time.Second
)

var defaultStairLevels = []int{1, 2, 4, 8, 16}

type cliOptions struct {
	concurrency int
	requests    int
	profileName string
	models      []string
}

func main() {
	opts := parseFlags()

	printAppHeader()

	cfg, err := config.Load()
	if err != nil {
		die("加载配置失败: %v", err)
	}

	ctx, cancel := runner.InstallSignalHandler()
	defer cancel()

	if opts.concurrency > 0 {
		runFromFlags(ctx, cfg, opts)
		return
	}

	runInteractive(ctx, cfg, opts)
}

func parseFlags() cliOptions {
	var opts cliOptions
	flag.IntVar(&opts.concurrency, "t", 0, "固定并发线程数；设置后不进入交互选择，直接跑固定并发")
	flag.IntVar(&opts.requests, "n", defaultRequests, "总请求数；固定并发为总量，交互阶梯扫描为每档请求数")
	flag.StringVar(&opts.profileName, "profile", "", "使用指定 profile 名称")
	var modelFlags modelFlag
	flag.Var(&modelFlags, "model", "使用指定模型 ID；多个模型可用逗号分隔或重复传入")
	profileShort := flag.String("p", "", "使用指定 profile 名称（简写）")
	flag.Var(&modelFlags, "m", "使用指定模型 ID（简写）；多个模型可用逗号分隔或重复传入")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "用法:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  %s                  交互选择 provider/model，默认跑阶梯扫描\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -t 8 -n 50       使用默认 profile 和上次模型，直接跑固定并发\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -p example-provider -m glm-5.2 -t 8 -n 50\n\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -p example-provider -m glm-5.2,kimi-2.6 -t 8 -n 50\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if *profileShort != "" {
		opts.profileName = *profileShort
	}
	opts.models = modelFlags.Models()
	if flag.NArg() > 0 {
		die("不支持的位置参数: %s", strings.Join(flag.Args(), " "))
	}
	if opts.concurrency < 0 {
		die("-t 必须大于 0")
	}
	if opts.requests <= 0 {
		die("-n 必须大于 0")
	}
	return opts
}

func runInteractive(ctx context.Context, cfg *config.File, opts cliOptions) {
	profile := selectProfile(cfg)
	cfg.SetDefault(profile.Name)
	if err := cfg.Save(); err != nil {
		fmt.Printf("保存 config.json 失败: %v\n", err)
	}
	printProvider(profile)

	client := api.New(profile.BaseURL, profile.APIKey)

	models := selectModels(ctx, client)
	if len(models) == 0 {
		die("模型 ID 不能为空")
	}
	cfg.SetLastModel(profile.Name, strings.Join(models, ","))
	if err := cfg.Save(); err != nil {
		fmt.Printf("保存 config.json 失败: %v\n", err)
	}
	printModels(models)

	params := testParams{
		requests:    int64(opts.requests),
		stairLevels: append([]int(nil), defaultStairLevels...),
	}
	printBuiltinSettings("staircase", fmt.Sprintf("levels=%v  requests/level=%d", params.stairLevels, params.requests))
	runStaircaseForModels(ctx, client, params, models)
}

func runFromFlags(ctx context.Context, cfg *config.File, opts cliOptions) {
	profile := selectProfileFromFlags(cfg, opts.profileName)
	cfg.SetDefault(profile.Name)
	printProvider(profile)

	models := opts.models
	if len(models) == 0 {
		models = splitModels(profile.LastModel)
	}
	if len(models) == 0 {
		die("参数模式缺少模型：请加 -m <model>，或先运行一次交互模式选择模型以记录 last_model")
	}

	cfg.SetLastModel(profile.Name, strings.Join(models, ","))
	if err := cfg.Save(); err != nil {
		fmt.Printf("保存 config.json 失败: %v\n", err)
	}

	printModels(models)

	client := api.New(profile.BaseURL, profile.APIKey)
	params := testParams{
		concurrency: opts.concurrency,
		requests:    int64(opts.requests),
	}
	printBuiltinSettings("fixed concurrency", fmt.Sprintf("threads=%d  requests=%d", params.concurrency, params.requests))
	runFixedForModels(ctx, client, params, models)
}

// ---- 流程步骤 ----

func selectProfile(cfg *config.File) config.Profile {
	if len(cfg.Profiles) == 0 {
		fmt.Println("\n未发现任何 profile，先新建一个。")
		return createProfile(cfg)
	}
	opts := make([]ui.SelectOption, len(cfg.Profiles))
	for i, p := range cfg.Profiles {
		opts[i] = ui.SelectOption{Label: p.Name, Desc: p.BaseURL}
	}
	idx, isNew := ui.Select("选择 profile：", opts, true)
	if isNew {
		return createProfile(cfg)
	}
	return cfg.Profiles[idx]
}

func createProfile(cfg *config.File) config.Profile {
	fmt.Println("\n--- 新建 profile ---")
	name := ui.Prompt("名称 (例如 openai / deepseek / local)", "default")
	if name == "" {
		name = "default"
	}
	baseURL := ui.Prompt("Base URL (例如 https://api.openai.com/v1)", "")
	for baseURL == "" {
		fmt.Println("  Base URL 不能为空")
		baseURL = ui.Prompt("Base URL", "")
	}
	apiKey := ui.Prompt("API Key (可留空，用于本地无鉴权部署)", "")
	p := config.Profile{Name: name, BaseURL: baseURL, APIKey: apiKey}
	cfg.AddProfile(p)
	return p
}

func selectModels(ctx context.Context, client *api.Client) []string {
	fmt.Println("\n正在查询可用模型...")
	models, err := client.ListModels(ctx)
	if err != nil {
		fmt.Printf("查询模型失败: %v\n", err)
		fmt.Println("将手动输入模型 ID。")
		return splitModels(ui.Prompt("模型 ID（多个用逗号分隔）", ""))
	}
	if len(models) == 0 {
		fmt.Println("接口返回空模型列表。")
		return splitModels(ui.Prompt("模型 ID（多个用逗号分隔）", ""))
	}
	opts := make([]ui.SelectOption, len(models))
	for i, m := range models {
		desc := m.OwnedBy
		opts[i] = ui.SelectOption{Label: m.ID, Desc: desc}
	}
	idxs := ui.MultiSelect(fmt.Sprintf("选择模型（共 %d 个）：", len(opts)), opts)
	out := make([]string, 0, len(idxs))
	for _, idx := range idxs {
		out = append(out, opts[idx].Label)
	}
	return out
}

type testParams struct {
	concurrency int
	requests    int64
	stairLevels []int
}

type modelFlag struct {
	values []string
}

func (m *modelFlag) String() string {
	return strings.Join(m.values, ",")
}

func (m *modelFlag) Set(value string) error {
	m.values = append(m.values, splitModels(value)...)
	return nil
}

func (m *modelFlag) Models() []string {
	return dedupeStrings(m.values)
}

func selectProfileFromFlags(cfg *config.File, name string) config.Profile {
	if name != "" {
		p, ok := cfg.Find(name)
		if !ok {
			die("未找到 profile %q，请先运行交互模式创建，或检查 config.json", name)
		}
		return p
	}
	if p, ok := cfg.DefaultProfile(); ok {
		return p
	}
	die("未发现 profile：请先运行 ./llm_pressure 创建 provider 配置")
	return config.Profile{}
}

func defaultChatRequest(model string) api.ChatRequest {
	return api.ChatRequest{
		Model:       model,
		Messages:    []api.Message{{Role: "user", Content: defaultPrompt}},
		MaxTokens:   defaultMaxTokens,
		Temperature: defaultTemperature,
	}
}

func printBuiltinSettings(mode, detail string) {
	fmt.Println()
	fmt.Printf("%s %s\n", colorLabel("Preset", 11), mode)
	fmt.Printf("%s max=%d  temperature=%.1f\n", colorLabel("Tokens", 11), defaultMaxTokens, defaultTemperature)
	fmt.Printf("%s %s\n", colorLabel("Run", 11), detail)
	fmt.Printf("%s %s\n", colorLabel("Modes", 11), "auto-detect non-stream and stream")
}

func runFixedForModels(ctx context.Context, client *api.Client, p testParams, models []string) {
	var all []runner.Stats
	for idx, model := range models {
		if ctx.Err() != nil {
			break
		}
		printModelRunHeader(idx+1, len(models), model)
		all = append(all, runFixedAuto(ctx, client, defaultChatRequest(model), p, model)...)
	}
	runner.PrintModelSummary(all)
}

func runFixedAuto(ctx context.Context, client *api.Client, req api.ChatRequest, p testParams, model string) []runner.Stats {
	modes := detectSupportedModes(ctx, client, req)
	var summaries []runner.Stats
	for _, stream := range modes {
		if ctx.Err() != nil {
			return summaries
		}
		if stats, ok := runFixed(ctx, client, req, p, model, stream); ok {
			summaries = append(summaries, stats)
		}
	}
	runner.PrintComparison(summaries)
	return summaries
}

func runStaircaseForModels(ctx context.Context, client *api.Client, p testParams, models []string) {
	var all []runner.StaircaseResult
	for idx, model := range models {
		if ctx.Err() != nil {
			break
		}
		printModelRunHeader(idx+1, len(models), model)
		all = append(all, runStaircaseAuto(ctx, client, defaultChatRequest(model), p, model)...)
	}
	runner.PrintStaircaseModelSummary(all)
}

func runStaircaseAuto(ctx context.Context, client *api.Client, req api.ChatRequest, p testParams, model string) []runner.StaircaseResult {
	modes := detectSupportedModes(ctx, client, req)
	var all []runner.StaircaseResult
	for _, stream := range modes {
		if ctx.Err() != nil {
			return all
		}
		all = append(all, runStaircase(ctx, client, req, p, model, stream)...)
	}
	return all
}

func detectSupportedModes(ctx context.Context, client *api.Client, req api.ChatRequest) []bool {
	fmt.Println()
	fmt.Println(term.Cyan("Checking API modes..."))
	probeReq := req
	probeReq.Messages = []api.Message{{Role: "user", Content: "Reply with one short sentence."}}
	probeReq.MaxTokens = probeMaxTokens

	candidates := []bool{false, true}
	supported := make([]bool, 0, len(candidates))
	for _, stream := range candidates {
		if ctx.Err() != nil {
			return supported
		}
		probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		var r api.Result
		if stream {
			r = client.ChatStream(probeCtx, probeReq)
		} else {
			r = client.Chat(probeCtx, probeReq)
		}
		cancel()
		if r.Err != nil {
			fmt.Printf("  %s %-11s %s\n", term.Red("x"), modeName(stream), compactForLine(r.Err.Error(), 180))
			continue
		}
		fmt.Printf("  %s %-11s available\n", term.Green("✓"), modeName(stream))
		supported = append(supported, stream)
	}
	if len(supported) == 0 {
		fmt.Println("\n没有可用的请求模式，压测已跳过。")
	}
	return supported
}

func runFixed(ctx context.Context, client *api.Client, req api.ChatRequest, p testParams, model string, stream bool) (runner.Stats, bool) {
	progressCb := makeProgressCB()
	fmt.Printf("\n%s %s  concurrency=%d  requests=%d\n",
		term.Cyan("Running"), modeName(stream), p.concurrency, p.requests)

	stats, samples := runner.RunFixed(ctx, runner.RunConfig{
		Client:      client,
		Req:         req,
		Stream:      stream,
		Concurrency: p.concurrency,
		Requests:    p.requests,
		Model:       model,
		OnProgress:  progressCb,
	})
	clearProgressLine()
	runner.PrintFixed(stats)

	rep := runner.Report{
		Timestamp: time.Now().Format("20060102_150405"),
		Model:     model,
		Stream:    stream,
		Mode:      "fixed",
		Request:   reportRequestFrom(req),
		Fixed:     &stats,
	}
	if path, err := runner.SaveReport(rep); err == nil {
		fmt.Printf("\n%s %s  (%d samples)\n", term.Gray("Saved"), path, len(samples))
		_ = samples
	} else {
		fmt.Printf("\n保存报告失败: %v\n", err)
	}
	return stats, true
}

func runStaircase(ctx context.Context, client *api.Client, req api.ChatRequest, p testParams, model string, stream bool) []runner.StaircaseResult {
	perLevelRequests := p.requests

	progressCb := makeProgressCB()
	fmt.Printf("\n%s staircase %s  levels=%v  requests/level=%d\n",
		term.Cyan("Running"), modeName(stream), p.stairLevels, perLevelRequests)

	results := runner.RunStaircase(ctx, runner.StaircaseConfig{
		Client:           client,
		Req:              req,
		Stream:           stream,
		Levels:           p.stairLevels,
		RequestsPerLevel: perLevelRequests,
		CoolDown:         3 * time.Second,
		Model:            model,
		OnLevelStart: func(level, conc int) {
			fmt.Printf("\n%s level=%d  concurrency=%d\n", term.Cyan("Level"), level, conc)
		},
		OnProgress: progressCb,
	})
	clearProgressLine()
	runner.PrintStaircase(results, stream)

	rep := runner.Report{
		Timestamp: time.Now().Format("20060102_150405"),
		Model:     model,
		Stream:    stream,
		Mode:      "staircase",
		Request:   reportRequestFrom(req),
		Staircase: results,
	}
	if path, err := runner.SaveReport(rep); err == nil {
		fmt.Printf("\n%s %s  (%d levels)\n", term.Gray("Saved"), path, len(results))
	} else {
		fmt.Printf("\n保存报告失败: %v\n", err)
	}
	return results
}

func makeProgressCB() func(runner.Progress) {
	if !term.IsTerminal() {
		return nil
	}
	lastLen := 0
	return func(p runner.Progress) {
		line := fmt.Sprintf("  progress %d done   %d ok   %d failed   %d tok",
			p.Done, p.OK, p.Failed, p.Tokens)
		pad := ""
		if len(line) < lastLen {
			pad = strings.Repeat(" ", lastLen-len(line))
		}
		fmt.Printf("\r%s%s", line, pad)
		lastLen = len(line)
	}
}

func clearProgressLine() {
	if !term.IsTerminal() {
		return
	}
	fmt.Print("\r" + strings.Repeat(" ", 100) + "\r")
}

func compactForLine(s string, maxLen int) string {
	s = strings.Join(strings.Fields(s), " ")
	if maxLen > 0 && len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}

func splitModels(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		model := strings.TrimSpace(part)
		if model == "" {
			continue
		}
		out = append(out, model)
	}
	return dedupeStrings(out)
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func reportRequestFrom(req api.ChatRequest) runner.ReportRequest {
	out := runner.ReportRequest{
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}
	if len(req.Messages) > 0 {
		out.Prompt = req.Messages[0].Content
	}
	return out
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

func printAppHeader() {
	fmt.Println("╭──────────────────────────────────────────────╮")
	fmt.Printf("│  %s%s│\n", term.Bold(term.Cyan("LLM Pressure Test")), strings.Repeat(" ", 26))
	fmt.Println("╰──────────────────────────────────────────────╯")
}

func printProvider(profile config.Profile) {
	fmt.Println()
	fmt.Printf("%s %s\n", colorLabel("Provider", 11), profile.Name)
	fmt.Printf("%s %s\n", colorLabel("Base URL", 11), profile.BaseURL)
}

func printModel(model string) {
	fmt.Printf("%s %s\n", colorLabel("Model", 11), model)
}

func printModels(models []string) {
	if len(models) == 1 {
		printModel(models[0])
		return
	}
	fmt.Printf("%s %d selected\n", colorLabel("Models", 11), len(models))
	for i, model := range models {
		fmt.Printf("  %2d. %s\n", i+1, model)
	}
}

func printModelRunHeader(current, total int, model string) {
	fmt.Println()
	fmt.Printf("%s %d/%d  %s\n", term.Cyan("Model"), current, total, model)
}

func colorLabel(s string, width int) string {
	if len(s) < width {
		s += strings.Repeat(" ", width-len(s))
	}
	return term.Cyan(s)
}

func die(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
	os.Exit(1)
}
