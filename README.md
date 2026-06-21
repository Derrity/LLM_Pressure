# LLM_Pressure

LLM Chat Completion 接口压力测试工具，用 Go 编写，零第三方依赖（纯标准库）。

针对 OpenAI 兼容的 `/v1/chat/completions` 接口，测量**延迟**、**token 输出速度**，以及**并发下的每线程速度与总吞吐**。

---

## 功能特性

- **交互式 profile 管理**：启动时选择已存的 base URL + API key，没有则引导新建，落盘到 `config.json`
- **自动发现模型**：调用 `/models` 拉取列表，支持关键字筛选后选择
- **两种压测模式**
  - `固定并发`：N 个线程持续发请求，按总请求数或总时长停止
  - `阶梯扫描`：依次跑 1, 2, 4, 8, 16… 并发（档位可自定义），输出对比表
- **流式 / 非流式分别统计**
  - 流式（`stream: true`）：测量首 token 延迟（TTFT）、生成耗时、token 速率
  - 非流式：测量端到端延迟与整体 tokens/s
- **推理模型支持**：自动累计 `reasoning_content` token（GLM-4.7 / DeepSeek-R1 等）
- **完整统计**
  - 单请求：端到端延迟、TTFT、生成耗时、单请求 TPS
  - 聚合：p50 / p95 / p99 / max / mean、总吞吐、每线程 TPS、错误分布
- **优雅中断**：Ctrl+C 后仍输出已采集的统计
- **结果落盘**：原始样本保存到 `results/{timestamp}_{mode}.json`
- **安全**：`config.json` 与 `results/` 默认 gitignore，API key 不会入库

---

## 环境要求

- Go 1.21+（用了 `atomic.Int64`，需要 1.19+；建议 1.21+）
- 一个 OpenAI 兼容的推理服务端点

---

## 编译与运行

```bash
# 直接运行
go run .

# 或编译为二进制
go build -o llm_pressure .
./llm_pressure
```

---

## 使用流程

启动后按交互提示依次完成：

1. **选择 profile**
   - 首次运行无 `config.json`，引导输入：名称、Base URL、API Key
   - 已有配置则列表选择，或选 `+ 新建` 追加一个

2. **选择模型**
   - 自动 `GET /models`，列出所有可用模型
   - 可输入关键字筛选（如 `glm`、`gpt`），再编号选择

3. **配置测试参数**
   - Prompt（带默认值）
   - `max_tokens`、`temperature`
   - 并发数 N
   - 停止条件：按总请求数 / 按总时长
   - 是否自定义阶梯档位（默认 `1,2,4,8,16`）

4. **选择模式**：固定并发 / 阶梯扫描

5. **选择流式 / 非流式**

6. **运行**：实时显示进度（已完成 / 成功 / 失败 / 累计 token）

7. **输出报告**：终端打印统计表，并保存 JSON 到 `results/`

---

## 配置文件

`config.json`（运行目录下，权限 `0600`，已 gitignore）：

```json
{
  "profiles": [
    {
      "name": "my-provider",
      "base_url": "https://api.example.com/v1",
      "api_key": "sk-..."
    },
    {
      "name": "local",
      "base_url": "http://127.0.0.1:8080/v1",
      "api_key": ""
    }
  ],
  "default": "my-provider"
}
```

- 支持多个 profile，启动时切换
- `api_key` 可留空（用于本地无鉴权部署）
- 同名 profile 再次新建会覆盖旧值

---

## 输出指标说明

### 单次请求采样
| 指标 | 流式 | 非流式 | 说明 |
|---|---|---|---|
| `端到端延迟` | ✓ | ✓ | 从请求发出到响应完成 |
| `首 token 延迟 (TTFT)` | ✓ | ✗ | 首个 token（含 reasoning）到达时间 |
| `生成耗时` | ✓ | =端到端 | `端到端 - TTFT` |
| `完成 token` | ✓ | ✓ | 优先取 `usage.completion_tokens`，否则估算 |
| `单请求 TPS` | ✓ | ✓ | `完成 token / 生成耗时` |

### 聚合统计
- **延迟分布**：p50 / p95 / p99 / max / mean
- **总吞吐**：`Σ完成 token / 测试窗口总时长`（反映并发下的真实出字速度）
- **每线程 TPS**：每个 worker 各自的 `完成 token / 该 worker 活跃时长`，末尾附 `SUM/avg` 汇总行
- **错误分布**：按错误信息归类计数

### 阶梯扫描汇总表
对比各并发档位的：总耗时、总吞吐、p95 端到端、p95 TTFT、失败率。

---

## 报告示例（流式固定并发）

```
══════════════════════════════════════════════════════════════════════
  模型: gpt-4o-mini   |  模式: 固定并发  [流式]  |  并发: 2
  总耗时: 20.195s   |  请求: 4 (成功 4 / 失败 0)
  完成 token: 2048   |  prompt token: 108   |  总吞吐: 101.41 tokens/s
──────────────────────────────────────────────────────────────────────
  延迟分布:
  端到端              n=4  min=8.165s  p50=8.27s  p95=9.271s  p99=9.271s  max=10.924s  mean=9.157s
  首 token (TTFT)   n=4  min=437ms  p50=648ms  p95=930ms  p99=930ms  max=3.028s  mean=1.261s
  生成耗时             n=4  min=7.516s  p50=7.834s  p95=7.897s  p99=7.897s  max=8.341s  mean=7.897s
  单请求 TPS          n=4  min=61.38  p50=64.84  p95=65.36  p99=65.36  max=68.12  mean=64.92  tokens/s
──────────────────────────────────────────────────────────────────────
  每线程吞吐:
    worker            请求数         成功      完成token            TPS
    0                   2          2         1024          50.71
    1                   2          2         1024          62.31
    SUM/avg             4          4         2048          55.91
──────────────────────────────────────────────────────────────────────
```

---

## 项目结构

```
LLM_Pressure/
├── go.mod                  # module llm_pressure, 零第三方依赖
├── main.go                 # 入口 & 交互流程编排
├── config/config.go        # config.json 读写 / profile 管理
├── api/client.go           # OpenAI 兼容客户端 + SSE 解析
├── ui/prompt.go            # bufio 交互菜单
├── runner/
│   ├── runner.go           # 固定并发 + 阶梯扫描执行器
│   ├── worker.go           # 单 worker 采样循环
│   ├── stats.go            # 聚合统计（百分位、每线程、总吞吐）
│   └── report.go           # 表格输出 + JSON 落盘
├── config.json             # 本地配置（gitignore，不入库）
└── results/                # 测试报告（gitignore）
```

---

## 注意事项

- **token 计数**：优先使用接口返回的 `usage.completion_tokens`；若上游不返回 `usage`，则按空格分词估算（英文较准，中文偏低，报告会标注 `⚠ 估算值`）
- **推理模型**：GLM-4.7 / DeepSeek-R1 等会把思考过程放在 `reasoning_content` 里，本工具将其计入完成 token（与上游 `usage` 口径一致）。注意 `max_tokens` 要给够，否则可能被 reasoning 用尽而拿不到正式回答
- **超时**：HTTP 客户端默认 10 分钟，流式长输出不会被过早打断
- **中断**：Ctrl+C（SIGINT）后停止发新请求，已采集的样本仍会聚合输出
- **安全**：`config.json` 含 API key，已加入 `.gitignore`；切勿手动提交

---

## 许可

MIT
