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
	model       string
}

func main() {
	opts := parseFlags()

	fmt.Println("╔══════════════════════════════════════════╗")
	fmt.Println("║       LLM Chat Completion 压力测试       ║")
	fmt.Println("╚══════════════════════════════════════════╝")

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
	flag.StringVar(&opts.model, "model", "", "使用指定模型 ID")
	profileShort := flag.String("p", "", "使用指定 profile 名称（简写）")
	modelShort := flag.String("m", "", "使用指定模型 ID（简写）")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "用法:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  %s                  交互选择 provider/model，默认跑阶梯扫描\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -t 8 -n 50       使用默认 profile 和上次模型，直接跑固定并发\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -p example-provider -m glm-5.2 -t 8 -n 50\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if *profileShort != "" {
		opts.profileName = *profileShort
	}
	if *modelShort != "" {
		opts.model = *modelShort
	}
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
	fmt.Printf("\n使用 profile: %s\n  base_url: %s\n", profile.Name, profile.BaseURL)

	client := api.New(profile.BaseURL, profile.APIKey)

	model := selectModel(ctx, client)
	if model == "" {
		die("模型 ID 不能为空")
	}
	cfg.SetLastModel(profile.Name, model)
	if err := cfg.Save(); err != nil {
		fmt.Printf("保存 config.json 失败: %v\n", err)
	}
	fmt.Printf("\n已选模型: %s\n", model)

	params := testParams{
		requests:    int64(opts.requests),
		stairLevels: append([]int(nil), defaultStairLevels...),
	}
	req := defaultChatRequest(model)

	printBuiltinSettings("交互模式: 默认阶梯扫描", fmt.Sprintf("档位=%v  每档请求=%d", params.stairLevels, params.requests))
	runStaircaseAuto(ctx, client, req, params, model)
}

func runFromFlags(ctx context.Context, cfg *config.File, opts cliOptions) {
	profile := selectProfileFromFlags(cfg, opts.profileName)
	cfg.SetDefault(profile.Name)
	fmt.Printf("\n使用 profile: %s\n  base_url: %s\n", profile.Name, profile.BaseURL)

	model := opts.model
	if model == "" {
		model = profile.LastModel
	}
	if model == "" {
		die("参数模式缺少模型：请加 -m <model>，或先运行一次交互模式选择模型以记录 last_model")
	}

	cfg.SetLastModel(profile.Name, model)
	if err := cfg.Save(); err != nil {
		fmt.Printf("保存 config.json 失败: %v\n", err)
	}

	fmt.Printf("\n已选模型: %s\n", model)

	client := api.New(profile.BaseURL, profile.APIKey)
	params := testParams{
		concurrency: opts.concurrency,
		requests:    int64(opts.requests),
	}
	req := defaultChatRequest(model)

	printBuiltinSettings("参数模式: 固定并发", fmt.Sprintf("并发=%d  总请求=%d", params.concurrency, params.requests))
	runFixedAuto(ctx, client, req, params, model)
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

func selectModel(ctx context.Context, client *api.Client) string {
	fmt.Println("\n正在查询可用模型...")
	models, err := client.ListModels(ctx)
	if err != nil {
		fmt.Printf("查询模型失败: %v\n", err)
		fmt.Println("将手动输入模型 ID。")
		return ui.Prompt("模型 ID", "")
	}
	if len(models) == 0 {
		fmt.Println("接口返回空模型列表。")
		return ui.Prompt("模型 ID", "")
	}
	opts := make([]ui.SelectOption, len(models))
	for i, m := range models {
		desc := m.OwnedBy
		opts[i] = ui.SelectOption{Label: m.ID, Desc: desc}
	}
	idx, _ := ui.Select(fmt.Sprintf("选择模型（共 %d 个）：", len(opts)), opts, false)
	return opts[idx].Label
}

type testParams struct {
	concurrency int
	requests    int64
	stairLevels []int
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
	fmt.Printf("\n%s\n", mode)
	fmt.Printf("内置请求参数: max_tokens=%d  temperature=%.1f  prompt=内置故事生成\n", defaultMaxTokens, defaultTemperature)
	fmt.Printf("%s\n", detail)
	fmt.Println("流式/非流式将自动检测；可用的模式都会执行，不可用的会跳过。")
}

func runFixedAuto(ctx context.Context, client *api.Client, req api.ChatRequest, p testParams, model string) {
	modes := detectSupportedModes(ctx, client, req)
	for _, stream := range modes {
		if ctx.Err() != nil {
			return
		}
		runFixed(ctx, client, req, p, model, stream)
	}
}

func runStaircaseAuto(ctx context.Context, client *api.Client, req api.ChatRequest, p testParams, model string) {
	modes := detectSupportedModes(ctx, client, req)
	for _, stream := range modes {
		if ctx.Err() != nil {
			return
		}
		runStaircase(ctx, client, req, p, model, stream)
	}
}

func detectSupportedModes(ctx context.Context, client *api.Client, req api.ChatRequest) []bool {
	fmt.Println("\n正在检测请求模式...")
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
			fmt.Printf("  跳过 %s: %s\n", streamLabel(stream), compactForLine(r.Err.Error(), 180))
			continue
		}
		fmt.Printf("  可用 %s\n", streamLabel(stream))
		supported = append(supported, stream)
	}
	if len(supported) == 0 {
		fmt.Println("\n没有可用的请求模式，压测已跳过。")
	}
	return supported
}

func runFixed(ctx context.Context, client *api.Client, req api.ChatRequest, p testParams, model string, stream bool) {
	progressCb := makeProgressCB()
	fmt.Printf("\n开始固定并发：并发=%d  总请求=%d  模型=%s  %s\n",
		p.concurrency, p.requests, model, streamLabel(stream))

	stats, samples := runner.RunFixed(ctx, runner.RunConfig{
		Client:      client,
		Req:         req,
		Stream:      stream,
		Concurrency: p.concurrency,
		Requests:    p.requests,
		Model:       model,
		OnProgress:  progressCb,
	})
	fmt.Println()
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
		fmt.Printf("\n原始样本已保存: %s  (共 %d 条样本)\n", path, len(samples))
		_ = samples
	} else {
		fmt.Printf("\n保存报告失败: %v\n", err)
	}
}

func runStaircase(ctx context.Context, client *api.Client, req api.ChatRequest, p testParams, model string, stream bool) {
	perLevelRequests := p.requests

	progressCb := makeProgressCB()
	fmt.Printf("\n开始阶梯扫描：档位=%v  每档请求=%d  模型=%s  %s\n",
		p.stairLevels, perLevelRequests, model, streamLabel(stream))

	results := runner.RunStaircase(ctx, runner.StaircaseConfig{
		Client:           client,
		Req:              req,
		Stream:           stream,
		Levels:           p.stairLevels,
		RequestsPerLevel: perLevelRequests,
		CoolDown:         3 * time.Second,
		Model:            model,
		OnLevelStart: func(level, conc int) {
			fmt.Printf("\n>>> 档 %d: 并发 = %d\n", level, conc)
		},
		OnProgress: progressCb,
	})
	fmt.Println()
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
		fmt.Printf("\n报告已保存: %s  (共 %d 档)\n", path, len(results))
	} else {
		fmt.Printf("\n保存报告失败: %v\n", err)
	}
}

func makeProgressCB() func(runner.Progress) {
	lastLen := 0
	return func(p runner.Progress) {
		line := fmt.Sprintf("  进度: 已完成 %d  成功 %d  失败 %d  累计 token %d",
			p.Done, p.OK, p.Failed, p.Tokens)
		// 用回车覆盖上一行
		pad := ""
		if len(line) < lastLen {
			pad = strings.Repeat(" ", lastLen-len(line))
		}
		fmt.Printf("\r%s%s", line, pad)
		lastLen = len(line)
	}
}

func compactForLine(s string, maxLen int) string {
	s = strings.Join(strings.Fields(s), " ")
	if maxLen > 0 && len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
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

func die(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
	os.Exit(1)
}
