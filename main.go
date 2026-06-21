package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"llm_pressure/api"
	"llm_pressure/config"
	"llm_pressure/runner"
	"llm_pressure/ui"
)

const defaultPrompt = "Write a short essay about the history of computing, covering the key milestones from mechanical calculators to modern AI."

func main() {
	fmt.Println("╔══════════════════════════════════════════╗")
	fmt.Println("║       LLM Chat Completion 压力测试       ║")
	fmt.Println("╚══════════════════════════════════════════╝")

	// 1. 加载/新建配置
	cfg, err := config.Load()
	if err != nil {
		die("加载配置失败: %v", err)
	}

	// 2. 选择 profile
	profile := selectProfile(cfg)
	if err := cfg.Save(); err != nil {
		fmt.Printf("⚠ 保存 config.json 失败: %v\n", err)
	}
	fmt.Printf("\n使用 profile: %s\n  base_url: %s\n", profile.Name, profile.BaseURL)

	client := api.New(profile.BaseURL, profile.APIKey)

	// 3. 查询并选择 model
	ctx, cancel := runner.InstallSignalHandler()
	defer cancel()

	model := selectModel(ctx, client)
	fmt.Printf("\n已选模型: %s\n", model)

	// 4. 选择测试参数
	params := collectParams()

	// 5. 选择模式
	mode := selectMode()

	req := api.ChatRequest{
		Model:       model,
		Messages:    []api.Message{{Role: "user", Content: params.prompt}},
		MaxTokens:   params.maxTokens,
		Temperature: params.temperature,
	}

	// 6. 执行
	if mode == "fixed" {
		runFixed(ctx, client, req, params, model)
	} else {
		runStaircase(ctx, client, req, params, model)
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
	// 支持模糊筛选：先问是否要筛选
	filter := ui.Prompt("\n可选：输入关键字筛选模型（回车跳过）", "")
	if filter != "" {
		filtered := make([]ui.SelectOption, 0)
		for _, o := range opts {
			if strings.Contains(strings.ToLower(o.Label), strings.ToLower(filter)) {
				filtered = append(filtered, o)
			}
		}
		if len(filtered) > 0 {
			opts = filtered
		} else {
			fmt.Println("  没有匹配的模型，使用完整列表。")
		}
	}
	idx, _ := ui.Select(fmt.Sprintf("选择模型（共 %d 个）：", len(opts)), opts, false)
	return opts[idx].Label
}

type testParams struct {
	prompt      string
	maxTokens   int
	temperature float64
	concurrency int
	requests    int64
	duration    time.Duration
	stairLevels []int
}

func collectParams() testParams {
	fmt.Println("\n--- 测试参数 ---")
	p := testParams{
		prompt:      ui.Prompt("Prompt", defaultPrompt),
		maxTokens:   ui.PromptInt("max_tokens", 256),
		temperature: ui.PromptFloat("temperature", 0.0),
	}
	p.concurrency = ui.PromptInt("并发数 N (1=单线程)", 8)

	stopMode, _ := ui.Select("停止条件：", []ui.SelectOption{
		{Label: "按总请求数", Desc: "完成 N 个请求后停止"},
		{Label: "按总时长", Desc: "跑满 T 秒后停止"},
	}, false)
	if stopMode == 0 {
		p.requests = int64(ui.PromptInt("总请求数", 50))
		p.duration = 0
	} else {
		p.duration = time.Duration(ui.PromptInt("总时长(秒)", 30)) * time.Second
		p.requests = 0
	}

	// 阶梯扫描的档位（即便选固定模式也问一下，便于复用）
	if ui.Confirm("是否自定义阶梯扫描的并发档位？（否则用默认 1,2,4,8,16）", false) {
		s := ui.Prompt("并发档位（逗号分隔，例如 1,2,4,8,16,32）", "1,2,4,8,16")
		p.stairLevels = parseLevels(s)
	} else {
		p.stairLevels = []int{1, 2, 4, 8, 16}
	}
	return p
}

func parseLevels(s string) []int {
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, x := range parts {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		var n int
		fmt.Sscanf(x, "%d", &n)
		if n > 0 {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return []int{1, 2, 4, 8, 16}
	}
	return out
}

func selectMode() string {
	idx, _ := ui.Select("选择测试模式：", []ui.SelectOption{
		{Label: "固定并发", Desc: "以 N 个线程持续发请求"},
		{Label: "阶梯扫描", Desc: "依次跑 1,2,4,8,16... 并发，输出对比表"},
	}, false)
	if idx == 0 {
		return "fixed"
	}
	return "staircase"
}

func runFixed(ctx context.Context, client *api.Client, req api.ChatRequest, p testParams, model string) {
	// 同时跑流式和非流式
	stream := ui.Confirm("\n使用流式 (stream:true)？(否=非流式)", true)

	progressCb := makeProgressCB(p.concurrency)
	fmt.Printf("\n开始压测：并发=%d  模型=%s  %s\n", p.concurrency, model, streamLabel(stream))

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
	fmt.Println()
	runner.PrintFixed(stats)

	rep := runner.Report{
		Timestamp: time.Now().Format("20060102_150405"),
		Model:     model,
		Stream:    stream,
		Mode:      "fixed",
		Fixed:     &stats,
	}
	if path, err := runner.SaveReport(rep); err == nil {
		fmt.Printf("\n原始样本已保存: %s  (共 %d 条样本)\n", path, len(samples))
		_ = samples
	} else {
		fmt.Printf("\n保存报告失败: %v\n", err)
	}
}

func runStaircase(ctx context.Context, client *api.Client, req api.ChatRequest, p testParams, model string) {
	stream := ui.Confirm("\n使用流式 (stream:true)？(否=非流式)", true)

	// 阶梯扫描时，每档使用用户设的「单档请求数」或「单档时长」
	// 用户输入的 p.requests / p.duration 视作「每档」
	perLevelRequests := p.requests
	perLevelDuration := p.duration
	if perLevelRequests == 0 && perLevelDuration == 0 {
		perLevelDuration = 20 * time.Second
	}

	progressCb := makeProgressCB(0)
	fmt.Printf("\n开始阶梯扫描：档位=%v  模型=%s  %s\n", p.stairLevels, model, streamLabel(stream))

	results := runner.RunStaircase(ctx, runner.StaircaseConfig{
		Client:           client,
		Req:              req,
		Stream:           stream,
		Levels:           p.stairLevels,
		RequestsPerLevel: perLevelRequests,
		DurationPerLevel: perLevelDuration,
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
		Staircase: results,
	}
	if path, err := runner.SaveReport(rep); err == nil {
		fmt.Printf("\n报告已保存: %s  (共 %d 档)\n", path, len(results))
	} else {
		fmt.Printf("\n保存报告失败: %v\n", err)
	}
}

func makeProgressCB(conc int) func(runner.Progress) {
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

func streamLabel(s bool) string {
	if s {
		return "[流式]"
	}
	return "[非流式]"
}

func die(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
}
