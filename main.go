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
	burnPrompt    = "Write the longest possible continuous original text you can within the token limit. Use dense English paragraphs, avoid markdown, avoid lists, do not summarize, do not stop early, and continue expanding the same story with concrete details until the response is complete."

	defaultRequests    = 32
	defaultMaxTokens   = 512
	defaultBurnTokens  = 4096
	defaultTemperature = 0.0
	probeMaxTokens     = 64
	probeTimeout       = 90 * time.Second
)

var defaultStairLevels = []int{1, 2, 4, 8, 16}

type cliOptions struct {
	concurrency int
	requests    int
	profileName string
	baseURL     string
	models      []string
	listModels  bool
	duration    time.Duration
	maxTokens   int
	burn        bool
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

	if opts.listModels {
		listModelsFromFlags(ctx, cfg, opts)
		return
	}

	if opts.concurrency > 0 || opts.burn {
		runFromFlags(ctx, cfg, opts)
		return
	}

	runInteractive(ctx, cfg, opts)
}

func parseFlags() cliOptions {
	var opts cliOptions
	args, listModels := preprocessListModelArgs(os.Args[1:])
	opts.listModels = listModels
	flag.IntVar(&opts.concurrency, "t", 0, "固定并发线程数；设置后不进入交互选择，直接跑固定并发")
	flag.IntVar(&opts.requests, "n", defaultRequests, "总请求数；固定并发为总量，交互阶梯扫描为每档请求数；固定并发下设为 0 表示持续运行直到 Ctrl+C")
	flag.StringVar(&opts.profileName, "profile", "", "使用指定 profile 名称")
	flag.StringVar(&opts.baseURL, "url", "", "临时 Base URL；可传 host、/v1，或完整 /v1/chat/completions")
	flag.BoolVar(&opts.listModels, "models", opts.listModels, "只列出可用模型 ID，不执行压测")
	flag.DurationVar(&opts.duration, "d", 0, "固定并发运行时长，例如 30s、10m、1h；设置后按时长停止")
	flag.IntVar(&opts.maxTokens, "max-tokens", defaultMaxTokens, "每次请求的 max_tokens")
	flag.BoolVar(&opts.burn, "burn", false, "持续高 token 消耗模式：默认 max_tokens=4096、长输出 prompt、一直跑到 Ctrl+C（可配合 -d 或 -n 限制）")
	var modelFlags modelFlag
	flag.Var(&modelFlags, "model", "使用指定模型 ID；多个模型可用逗号分隔或重复传入；不带值时列出模型 ID")
	profileShort := flag.String("p", "", "使用指定 profile 名称（简写）")
	urlShort := flag.String("u", "", "临时 Base URL（简写）；不写入 config.json")
	flag.Var(&modelFlags, "m", "使用指定模型 ID（简写）；多个模型可用逗号分隔或重复传入；不带值时列出模型 ID")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "用法:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  %s                  交互选择 provider/model，默认跑阶梯扫描\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -t 8 -n 32       使用默认 profile 和上次模型，直接跑固定并发\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -p example-provider -m                 列出该 provider 的模型 ID\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -u https://api.example.com/v1 -m       列出临时 URL 的模型 ID\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -p example-provider -m glm-5.2 -t 8 -n 32\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -p example-provider -m glm-5.2 -t 8 -burn\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -p example-provider -m glm-5.2 -t 8 -burn -d 30m\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -u https://api.example.com/v1/chat/completions -m glm-5.2 -t 8 -n 32\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -p example-provider -m glm-5.2,kimi-2.6 -t 8 -n 32\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.CommandLine.Parse(args)
	visited := visitedFlags()
	if *profileShort != "" {
		opts.profileName = *profileShort
	}
	if *urlShort != "" {
		opts.baseURL = *urlShort
	}
	if opts.burn {
		if opts.concurrency == 0 {
			opts.concurrency = 1
		}
		if !visited["max-tokens"] {
			opts.maxTokens = defaultBurnTokens
		}
		if !visited["n"] && opts.duration == 0 {
			opts.requests = 0
		}
	}
	if opts.duration > 0 {
		if visited["n"] {
			die("-d 和 -n 不能同时使用；按时长跑请只传 -d，按请求数跑请只传 -n")
		}
		opts.requests = 0
	}
	opts.models = modelFlags.Models()
	if flag.NArg() > 0 {
		die("不支持的位置参数: %s", strings.Join(flag.Args(), " "))
	}
	if opts.concurrency < 0 {
		die("-t 必须大于 0")
	}
	if opts.requests < 0 {
		die("-n 必须大于或等于 0")
	}
	if opts.concurrency == 0 && opts.requests == 0 {
		die("交互阶梯模式下 -n 必须大于 0；持续运行请使用 -t 或 -burn")
	}
	if opts.maxTokens <= 0 {
		die("-max-tokens 必须大于 0")
	}
	return opts
}

func visitedFlags() map[string]bool {
	visited := map[string]bool{}
	flag.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})
	return visited
}

func preprocessListModelArgs(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	listModels := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		if isBareModelFlag(arg, args, i) {
			listModels = true
			if i+1 < len(args) && args[i+1] == "" {
				i++
			}
			continue
		}
		out = append(out, arg)
	}
	return out, listModels
}

func isBareModelFlag(arg string, args []string, i int) bool {
	switch arg {
	case "-m", "--m", "-model", "--model":
		return i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-")
	case "-m=", "--m=", "-model=", "--model=":
		return true
	default:
		return false
	}
}

func runInteractive(ctx context.Context, cfg *config.File, opts cliOptions) {
	profile := selectProfile(cfg)
	profile.BaseURL = mustNormalizeBaseURL(profile.BaseURL)
	cfg.SetDefault(profile.Name)
	cfg.AddProfile(profile)
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
		maxTokens:   defaultMaxTokens,
		prompt:      defaultPrompt,
	}
	printBuiltinSettings("staircase", params.maxTokens, fmt.Sprintf("levels=%v  requests/level=%d", params.stairLevels, params.requests), false)
	runStaircaseForModels(ctx, client, params, models)
}

func runFromFlags(ctx context.Context, cfg *config.File, opts cliOptions) {
	profile := selectProfileFromFlags(cfg, opts.profileName, opts.baseURL)
	if opts.baseURL == "" {
		cfg.SetDefault(profile.Name)
	}
	printProvider(profile)

	models := opts.models
	if len(models) == 0 {
		models = splitModels(profile.LastModel)
	}
	if len(models) == 0 {
		die("参数模式缺少模型：请加 -m <model>，或先运行一次交互模式选择模型以记录 last_model")
	}

	if opts.baseURL == "" {
		cfg.SetLastModel(profile.Name, strings.Join(models, ","))
		if err := cfg.Save(); err != nil {
			fmt.Printf("保存 config.json 失败: %v\n", err)
		}
	}

	printModels(models)

	client := api.New(profile.BaseURL, profile.APIKey)
	params := testParams{
		concurrency: opts.concurrency,
		requests:    int64(opts.requests),
		duration:    opts.duration,
		maxTokens:   opts.maxTokens,
		prompt:      defaultPrompt,
		burn:        opts.burn,
	}
	if opts.burn {
		params.prompt = burnPrompt
	}
	printBuiltinSettings("fixed concurrency", params.maxTokens, fixedRunDetail(params), params.burn)
	runFixedForModels(ctx, client, params, models)
}

func listModelsFromFlags(ctx context.Context, cfg *config.File, opts cliOptions) {
	profile := selectProfileFromFlags(cfg, opts.profileName, opts.baseURL)
	printProvider(profile)

	client := api.New(profile.BaseURL, profile.APIKey)
	fmt.Println()
	fmt.Println(term.Cyan("Listing available model IDs..."))
	models, err := client.ListModels(ctx)
	if err != nil {
		die("查询模型失败: %v", err)
	}
	if len(models) == 0 {
		fmt.Println("未发现可用模型。")
		return
	}

	fmt.Printf("\n%s %d\n", colorLabel("Models", 11), len(models))
	for _, model := range models {
		fmt.Printf("  %s\n", model.ID)
	}
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
	baseURL = mustNormalizeBaseURL(baseURL)
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
	duration    time.Duration
	stairLevels []int
	maxTokens   int
	prompt      string
	burn        bool
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

func selectProfileFromFlags(cfg *config.File, name, baseURL string) config.Profile {
	if baseURL != "" {
		apiKey := strings.TrimSpace(os.Getenv("LLM_PRESSURE_API_KEY"))
		if apiKey == "" {
			apiKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
		}
		lastModel := ""
		if name != "" {
			p, ok := cfg.Find(name)
			if !ok {
				die("未找到 profile %q，请先运行交互模式创建，或检查 config.json", name)
			}
			apiKey = p.APIKey
			lastModel = p.LastModel
		}
		return config.Profile{
			Name:      "direct-url",
			BaseURL:   mustNormalizeBaseURL(baseURL),
			APIKey:    apiKey,
			LastModel: lastModel,
		}
	}
	if name != "" {
		p, ok := cfg.Find(name)
		if !ok {
			die("未找到 profile %q，请先运行交互模式创建，或检查 config.json", name)
		}
		p.BaseURL = mustNormalizeBaseURL(p.BaseURL)
		return p
	}
	if p, ok := cfg.DefaultProfile(); ok {
		p.BaseURL = mustNormalizeBaseURL(p.BaseURL)
		return p
	}
	die("未发现 profile：请先运行 ./llm_pressure 创建 provider 配置")
	return config.Profile{}
}

func mustNormalizeBaseURL(raw string) string {
	baseURL, err := api.NormalizeBaseURL(raw)
	if err != nil {
		die("%v", err)
	}
	return baseURL
}

func chatRequest(model string, p testParams) api.ChatRequest {
	maxTokens := p.maxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	prompt := p.prompt
	if prompt == "" {
		prompt = defaultPrompt
	}
	return api.ChatRequest{
		Model:       model,
		Messages:    []api.Message{{Role: "user", Content: prompt}},
		MaxTokens:   maxTokens,
		Temperature: defaultTemperature,
	}
}

func printBuiltinSettings(mode string, maxTokens int, detail string, burn bool) {
	fmt.Println()
	fmt.Printf("%s %s\n", colorLabel("Preset", 11), mode)
	fmt.Printf("%s max=%d  temperature=%.1f\n", colorLabel("Tokens", 11), maxTokens, defaultTemperature)
	fmt.Printf("%s %s\n", colorLabel("Run", 11), detail)
	fmt.Printf("%s %s\n", colorLabel("Modes", 11), "auto-detect non-stream and stream")
	if burn {
		fmt.Printf("%s %s\n", colorLabel("Burn", 11), "long-output prompt; press Ctrl+C to stop and print collected stats")
	}
}

func runFixedForModels(ctx context.Context, client *api.Client, p testParams, models []string) {
	var all []runner.Stats
	for idx, model := range models {
		if ctx.Err() != nil {
			break
		}
		printModelRunHeader(idx+1, len(models), model)
		all = append(all, runFixedAuto(ctx, client, chatRequest(model, p), p, model)...)
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
		all = append(all, runStaircaseAuto(ctx, client, chatRequest(model, p), p, model)...)
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
	fmt.Printf("\n%s %s  concurrency=%d  %s\n",
		term.Cyan("Running"), modeName(stream), p.concurrency, fixedStopLabel(p))

	stats, samples := runner.RunFixed(ctx, runner.RunConfig{
		Client:      client,
		Req:         req,
		Stream:      stream,
		Concurrency: p.concurrency,
		Requests:    p.requests,
		Duration:    p.duration,
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

func fixedRunDetail(p testParams) string {
	return fmt.Sprintf("threads=%d  %s", p.concurrency, fixedStopLabel(p))
}

func fixedStopLabel(p testParams) string {
	switch {
	case p.duration > 0:
		return fmt.Sprintf("duration=%s", p.duration)
	case p.requests > 0:
		return fmt.Sprintf("requests=%d", p.requests)
	default:
		return "requests=unlimited (Ctrl+C to stop)"
	}
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
