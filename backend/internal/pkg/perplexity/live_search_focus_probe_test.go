package perplexity

// 第 1 步可行性验证：Perplexity Web Ask 端点能否"按请求关掉联网搜索"。
//
// 这是接入 function tool 模拟方案的生死判定。模拟方案把工具协议说明塞进
// query_str，而 query_str 正是被联网搜索的字段（见 options.go BuildAskBodyWithOptions
// 唯一 body 构造器，params 里没有任何 system/instructions 通道）。如果 search_focus
// 能切到 "writing"（或清空 sources）让上游不再联网检索，query_str 就退化成纯 prompt，
// 工具协议不再被搜索污染，方案才算可行；否则每个工具请求都在和搜索抢输出。
//
// 本测试默认跳过（CI 不会跑），必须显式提供真实 cookie 才执行：
//
//	PPLX_COOKIE=<__Secure-next-auth.session-token 的值> \
//	  go test -tags=unit -run TestLiveSearchFocusProbe -v ./internal/pkg/perplexity/
//
// 可选环境变量：
//	PPLX_CSRF   next-auth.csrf-token 值（一般不需要）
//	PPLX_PROXY  出站代理 http(s)://... 或 socks5://...（账号有地区限制时用）
//	PPLX_MODEL  覆盖 model_preference（默认按 mode 选 turbo / pplx_pro）
//	PPLX_BASE   覆盖站点地址（默认 https://www.perplexity.ai）
//
// 它复用生产同款 uTLS 浏览器指纹（tlsfingerprint 默认 profile），所以结果不会
// 被 Cloudflare 的"裸 Go TLS = bot"误伤——判定的是协议语义，不是指纹对抗。
//
// 只读验证：不写数据库、不动账号状态、不换号。它只消耗一点上游订阅额度。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// probeSentinel 是探针口令：搜索引擎不会自然产出，用于判断"是否遵从了纯 prompt 指令"。
const probeSentinel = "PROBE_7Q_OK"

// probeQuery 明确要求只回口令、禁止联网与引用——既测"搜索是否被关"，也测格式遵从。
const probeQuery = "Reply with exactly this text and nothing else. Do not search the web. Do not add citations. The text is: " + probeSentinel

// searchFocusVariant 是一个待验证的参数组合。
type searchFocusVariant struct {
	name        string
	searchFocus string
	mode        string
	sources     []string
}

// 矩阵：分离出 search_focus / mode / sources 三个杠杆各自的作用。
var searchFocusVariants = []searchFocusVariant{
	{name: "baseline_internet_copilot", searchFocus: "internet", mode: ModeCopilot, sources: []string{"web"}},
	{name: "baseline_internet_concise", searchFocus: "internet", mode: ModeConcise, sources: []string{"web"}},
	{name: "writing_copilot_web", searchFocus: "writing", mode: ModeCopilot, sources: []string{"web"}},
	{name: "writing_copilot_nosrc", searchFocus: "writing", mode: ModeCopilot, sources: []string{}},
	{name: "writing_concise_nosrc", searchFocus: "writing", mode: ModeConcise, sources: []string{}},
}

func TestLiveSearchFocusProbe(t *testing.T) {
	session := strings.TrimSpace(os.Getenv("PPLX_COOKIE"))
	if session == "" {
		t.Skip("set PPLX_COOKIE=<session token> to run the live search_focus probe")
	}
	creds := Credentials{
		SessionToken: session,
		CSRFToken:    strings.TrimSpace(os.Getenv("PPLX_CSRF")),
		BaseURL:      strings.TrimSpace(os.Getenv("PPLX_BASE")),
	}
	client, err := newProbeClient(strings.TrimSpace(os.Getenv("PPLX_PROXY")))
	if err != nil {
		t.Fatalf("build probe http client: %v", err)
	}
	target := creds.NormalizedBaseURL() + AskPath

	t.Logf("endpoint=%s version=%s", target, askVersion)
	t.Logf("%-28s | %-4s | %-7s | %-5s | %-7s | %s", "variant", "HTTP", "sources", "steps", "adhered", "answer_preview")
	t.Logf("%s", strings.Repeat("-", 110))

	for _, v := range searchFocusVariants {
		v := v
		t.Run(v.name, func(t *testing.T) {
			res := runSearchFocusVariant(t, client, creds, target, v)
			adhered := "no"
			if res.adhered {
				adhered = "YES"
			}
			t.Logf("%-28s | %-4d | %-7d | %-5d | %-7s | %s",
				v.name, res.status, res.sourceCount, res.stepCount, adhered, oneLine(res.answerPreview, 60))
			if res.err != "" {
				t.Logf("  error: %s", res.err)
			}
			if res.status == http.StatusBadRequest {
				// 400 = 上游拒绝了该参数组合；body 会说明拒绝了哪个字段。
				t.Logf("  400 body: %s", oneLine(res.rawPreview, 300))
			}
		})
	}

	t.Logf("%s", strings.Repeat("-", 110))
	t.Logf("判读：某变体 sources=0 且 steps=0（且 HTTP=200）→ 该组合成功关闭联网搜索，模拟方案可行。")
	t.Logf("     adhered=YES → 该底座模型还能严格遵从纯 prompt 指令，格式遵从率值得进一步实测（第 2 步）。")
	t.Logf("     所有 writing 变体都 400 或仍有 sources → 协议无法关搜索，模拟方案不可行。")
}

// controlQuery 明确需要联网最新信息，且【不】写任何"禁止搜索"的话。用它做对照：
//   - internet 模式若返回 sources>0 → 证明该端点在"需要搜索的问题"上确实会搜索，
//     于是主探针里 internet 变体的 sources=0 是被 query 里"Do not search"指令关掉的
//     （即 query_str 指令被遵从，对工具模拟是强利好）。
//   - 同一 query 在 writing 模式返回 sources=0 → 证明 search_focus=writing 能强制关搜索。
//   - 若 internet 对照也 sources=0 → 说明本探针根本没触发搜索，主结论不成立，需另查。
const controlQuery = "What are the top world news headlines today? List two of them with their source website URLs."

// TestLiveSearchControlProbe 是 TestLiveSearchFocusProbe 的对照实验，消除"到底有没有在搜"的歧义。
func TestLiveSearchControlProbe(t *testing.T) {
	session := strings.TrimSpace(os.Getenv("PPLX_COOKIE"))
	if session == "" {
		t.Skip("set PPLX_COOKIE=<session token> to run the live control probe")
	}
	creds := Credentials{
		SessionToken: session,
		CSRFToken:    strings.TrimSpace(os.Getenv("PPLX_CSRF")),
		BaseURL:      strings.TrimSpace(os.Getenv("PPLX_BASE")),
	}
	client, err := newProbeClient(strings.TrimSpace(os.Getenv("PPLX_PROXY")))
	if err != nil {
		t.Fatalf("build probe http client: %v", err)
	}
	target := creds.NormalizedBaseURL() + AskPath

	controls := []searchFocusVariant{
		{name: "control_internet_force", searchFocus: "internet", mode: ModeCopilot, sources: []string{"web"}},
		{name: "control_writing_force", searchFocus: "writing", mode: ModeCopilot, sources: []string{}},
	}
	t.Logf("%-26s | %-4s | %-7s | %-5s | %s", "control", "HTTP", "sources", "steps", "answer_preview")
	t.Logf("%s", strings.Repeat("-", 100))
	for _, v := range controls {
		v := v
		t.Run(v.name, func(t *testing.T) {
			res := runControlVariant(t, client, creds, target, v)
			t.Logf("%-26s | %-4d | %-7d | %-5d | %s", v.name, res.status, res.sourceCount, res.stepCount, oneLine(res.answerPreview, 80))
			if res.err != "" {
				t.Logf("  error: %s", res.err)
			}
		})
	}
	t.Logf("%s", strings.Repeat("-", 100))
	t.Logf("判读：control_internet_force sources>0 且 control_writing_force sources=0 → 搜索可控，模拟方案可行。")
}

func runControlVariant(t *testing.T, client *http.Client, creds Credentials, target string, v searchFocusVariant) variantResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	body := buildProbeBody(controlQuery, probeModelPreference(v.mode), v.mode, v.searchFocus, v.sources, creds)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return variantResult{err: "build request: " + err.Error()}
	}
	creds.SetFetchStyleHeaders(req.Header, "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return variantResult{err: "do request: " + err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	res := variantResult{status: resp.StatusCode}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		res.rawPreview = string(raw)
		res.err = "non-200 status"
		return res
	}
	capped := &cappedBuffer{limit: 256 * 1024}
	decoder := NewDecoder(io.TeeReader(resp.Body, capped))
	var answer Answer
	var sources []Source
	for {
		if ctx.Err() != nil {
			res.err = "timeout/canceled before end_of_stream"
			break
		}
		ev, derr := decoder.Next()
		if derr != nil {
			if derr != io.EOF {
				res.err = "decode: " + derr.Error()
			}
			break
		}
		if len(ev.Sources) > 0 {
			sources = MergeSources(sources, ev.Sources)
		}
		res.stepCount += len(ev.Steps)
		if _, aerr := answer.Apply(ev, true); aerr != nil {
			res.err = "answer apply: " + aerr.Error()
			break
		}
		if ev.EndOfStream {
			break
		}
	}
	res.sourceCount = len(sources)
	res.answerPreview = answer.Content()
	return res
}

// ---------------------------------------------------------------------------
// 第 2 步：真实工具协议遵从率。用生产原语（ParseToolSpecs / ParseToolChoice /
// FoldMessagesWithTools / ToolCallParser）在 search_focus="writing" 下实测底座模型
// 是否按 <tool_call>{...}</tool_call> 协议作答、参数是否合法、该调时调/不该调时不调。
// ---------------------------------------------------------------------------

type toolScenario struct {
	name       string
	toolsJSON  string // CC tools 数组
	choiceJSON string // CC tool_choice（JSON 字面量，含引号）
	userMsg    string
	expectCall bool   // true=期望产出工具调用；false=期望纯文本（假阳性检查）
	expectName string // 期望被调用的工具名（expectCall 时校验；空=不校验具体名）
}

var toolScenarios = []toolScenario{
	{
		name:       "auto_single",
		toolsJSON:  `[{"type":"function","function":{"name":"get_weather","description":"Get current weather for a city","parameters":{"type":"object","properties":{"city":{"type":"string","description":"City name"}},"required":["city"]}}}]`,
		choiceJSON: `"auto"`,
		userMsg:    "What is the weather in Tokyo right now? Use the available tool.",
		expectCall: true,
		expectName: "get_weather",
	},
	{
		name:       "required_single",
		toolsJSON:  `[{"type":"function","function":{"name":"get_weather","description":"Get current weather for a city","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]`,
		choiceJSON: `"required"`,
		userMsg:    "Tell me something interesting about Paris.",
		expectCall: true,
		expectName: "get_weather",
	},
	{
		name:       "multi_select_nested",
		toolsJSON:  `[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}},{"type":"function","function":{"name":"get_stock_price","description":"Get the latest stock price for a ticker","parameters":{"type":"object","properties":{"symbol":{"type":"string","description":"Ticker symbol"},"exchange":{"type":"string","enum":["NASDAQ","NYSE"]}},"required":["symbol"]}}}]`,
		choiceJSON: `"auto"`,
		userMsg:    "What is the latest stock price of Apple (ticker AAPL on NASDAQ)? Use the right tool.",
		expectCall: true,
		expectName: "get_stock_price",
	},
	{
		name:       "no_call_falsepos",
		toolsJSON:  `[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]`,
		choiceJSON: `"auto"`,
		userMsg:    "Reply with a short friendly greeting. Do not call any tool.",
		expectCall: false,
		expectName: "",
	},
}

func TestLiveToolAdherenceProbe(t *testing.T) {
	session := strings.TrimSpace(os.Getenv("PPLX_COOKIE"))
	if session == "" {
		t.Skip("set PPLX_COOKIE=<session token> to run the live tool adherence probe")
	}
	creds := Credentials{
		SessionToken: session,
		CSRFToken:    strings.TrimSpace(os.Getenv("PPLX_CSRF")),
		BaseURL:      strings.TrimSpace(os.Getenv("PPLX_BASE")),
	}
	client, err := newProbeClient(strings.TrimSpace(os.Getenv("PPLX_PROXY")))
	if err != nil {
		t.Fatalf("build probe http client: %v", err)
	}
	target := creds.NormalizedBaseURL() + AskPath
	runs := 3
	if n, perr := strconv.Atoi(strings.TrimSpace(os.Getenv("PPLX_RUNS"))); perr == nil && n > 0 {
		runs = n
	}
	modelPref := probeModelPreference(ModeCopilot)

	t.Logf("tool adherence probe: model_preference=%s search_focus=writing runs=%d", modelPref, runs)
	t.Logf("%-20s | %-4s | %-9s | %-9s | %s", "scenario", "runs", "callrate", "passrate", "note")
	t.Logf("%s", strings.Repeat("-", 100))

	for _, sc := range toolScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			specs, err := ParseToolSpecs(gjson.Parse(sc.toolsJSON))
			if err != nil {
				t.Fatalf("ParseToolSpecs: %v", err)
			}
			choice, err := ParseToolChoice(gjson.Parse(sc.choiceJSON), specs)
			if err != nil {
				t.Fatalf("ParseToolChoice: %v", err)
			}
			query, err := FoldMessagesWithTools(
				gjson.Parse(`[{"role":"user","content":`+strconv.Quote(sc.userMsg)+`}]`),
				specs, choice, FoldOptions{StripSystem: true, MaxChars: DefaultMaxQueryChars},
			)
			if err != nil {
				t.Fatalf("FoldMessagesWithTools: %v", err)
			}

			calls, passes := 0, 0
			var firstFailRaw string
			for i := 0; i < runs; i++ {
				answer, status, ferr := fetchToolAnswer(client, creds, target, query, modelPref)
				if ferr != "" {
					t.Logf("  run %d: fetch error (http=%d): %s", i+1, status, ferr)
					if firstFailRaw == "" {
						firstFailRaw = "fetch error: " + ferr
					}
					continue
				}
				p := NewToolCallParser(choice.AllowedSpecs(specs))
				emitted := p.Feed(answer)
				rest, parsed := p.Finish()
				gotCall := len(parsed) > 0
				if gotCall {
					calls++
				}
				pass := evalToolRun(sc, choice, parsed)
				if pass {
					passes++
				} else if firstFailRaw == "" {
					if gotCall {
						firstFailRaw = "parsed name=" + parsed[0].Name + " args=" + parsed[0].Arguments
					} else {
						firstFailRaw = "no toolcall; raw=" + oneLine(emitted+rest, 200)
					}
				}
				detail := ""
				if gotCall {
					detail = parsed[0].Name + " " + oneLine(parsed[0].Arguments, 50)
				} else {
					detail = "text: " + oneLine(emitted+rest, 50)
				}
				t.Logf("  run %d: call=%v pass=%v | %s", i+1, gotCall, pass, detail)
			}
			note := ""
			if passes < runs && firstFailRaw != "" {
				note = "first-fail: " + firstFailRaw
			}
			t.Logf("%-20s | %-4d | %-9s | %-9s | %s",
				sc.name, runs, rateStr(calls, runs), rateStr(passes, runs), oneLine(note, 60))
		})
	}
	t.Logf("%s", strings.Repeat("-", 100))
	t.Logf("passrate = 期望行为达成率（该调时调对工具/不该调时不调 + 参数为合法 JSON 对象 + 满足 tool_choice）。")
	t.Logf("passrate 高 → 真实工具模拟遵从率可用；偏低看 first-fail 原文决定是否靠 prompt 微调或换底座模型（PPLX_MODEL）。")
}

// evalToolRun 判定一次运行是否达成场景预期。
func evalToolRun(sc toolScenario, choice ToolChoice, calls []ToolCall) bool {
	if !sc.expectCall {
		return len(calls) == 0
	}
	if len(calls) == 0 || !choice.Satisfied(calls) {
		return false
	}
	if sc.expectName != "" && calls[0].Name != sc.expectName {
		return false
	}
	// 参数必须是合法 JSON 对象（ToolCallParser 已 compact，这里再确认一次形状）。
	return gjson.Valid(calls[0].Arguments) && gjson.Parse(calls[0].Arguments).IsObject()
}

// fetchToolAnswer 在 writing 模式发一次工具请求，返回上游完整答案正文。
func fetchToolAnswer(client *http.Client, creds Credentials, target, query, modelPref string) (answer string, status int, errMsg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	body := buildProbeBody(query, modelPref, ModeCopilot, "writing", []string{}, creds)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return "", 0, "build request: " + err.Error()
	}
	creds.SetFetchStyleHeaders(req.Header, "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, "do request: " + err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	status = resp.StatusCode
	if status != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", status, "non-200: " + oneLine(string(raw), 200)
	}
	decoder := NewDecoder(resp.Body)
	var ans Answer
	for {
		if ctx.Err() != nil {
			return ans.Content(), status, "timeout before end_of_stream"
		}
		ev, derr := decoder.Next()
		if derr != nil {
			if derr == io.EOF {
				break
			}
			return ans.Content(), status, "decode: " + derr.Error()
		}
		if _, aerr := ans.Apply(ev, true); aerr != nil {
			return ans.Content(), status, "answer apply: " + aerr.Error()
		}
		if ev.EndOfStream {
			break
		}
	}
	return ans.Content(), status, ""
}

// TestLiveReasoningCapture 抓取 reasoning 模型的上游 SSE 真实形态：思维链是混在
// answer 里（<think>…</think>），还是独立 block/字段？决定"思考部分"的实现方式。
//
//	PPLX_COOKIE=... [PPLX_MODEL=pplx_reasoning] \
//	  go test -tags=unit -run TestLiveReasoningCapture -v ./internal/pkg/perplexity/
func TestLiveReasoningCapture(t *testing.T) {
	session := strings.TrimSpace(os.Getenv("PPLX_COOKIE"))
	if session == "" {
		t.Skip("set PPLX_COOKIE=<session token> to run the live reasoning capture")
	}
	creds := Credentials{SessionToken: session, CSRFToken: strings.TrimSpace(os.Getenv("PPLX_CSRF")), BaseURL: strings.TrimSpace(os.Getenv("PPLX_BASE"))}
	client, err := newProbeClient(strings.TrimSpace(os.Getenv("PPLX_PROXY")))
	if err != nil {
		t.Fatalf("build probe http client: %v", err)
	}
	target := creds.NormalizedBaseURL() + AskPath
	model := "pplx_reasoning"
	if m := strings.TrimSpace(os.Getenv("PPLX_MODEL")); m != "" {
		model = m
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	query := "A bat and a ball cost $1.10 in total. The bat costs $1.00 more than the ball. How much does the ball cost? Think step by step, then give the final answer."
	body := buildProbeBody(query, model, ModeCopilot, "internet", []string{"web"}, creds)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	creds.SetFetchStyleHeaders(req.Header, "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	t.Logf("model_preference=%s HTTP=%d", model, resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		t.Fatalf("non-200: %s", string(raw))
	}

	capped := &cappedBuffer{limit: 1024 * 1024}
	decoder := NewDecoder(io.TeeReader(resp.Body, capped))
	var answer Answer
	var planGoals []string
	cleanEOS := false
	decodeErr := "(none)"
	for {
		if ctx.Err() != nil {
			break
		}
		ev, derr := decoder.Next()
		if derr != nil {
			if derr != io.EOF {
				decodeErr = derr.Error()
			}
			break
		}
		for _, g := range ev.Plan {
			if txt := SafePlanReasoningText(g); txt != "" {
				planGoals = append(planGoals, txt)
			}
		}
		_, _ = answer.Apply(ev, true)
		if ev.EndOfStream {
			cleanEOS = true
			break
		}
	}
	t.Logf("干净终止=%v decodeErr=%s plan目标数=%d", cleanEOS, decodeErr, len(planGoals))
	for i, g := range planGoals {
		t.Logf("  plan[%d]: %s", i, oneLine(g, 90))
	}
	raw := capped.String()
	if dump := strings.TrimSpace(os.Getenv("PPLX_DUMP")); dump != "" {
		_ = os.WriteFile(dump, []byte(raw), 0o600)
		t.Logf("raw SSE 已写入 %s", dump)
	}

	// 统计出现过的 intended_usage 值（反映 block 结构）。
	usages := map[string]int{}
	for rest := raw; ; {
		i := strings.Index(rest, `"intended_usage":"`)
		if i < 0 {
			break
		}
		rest = rest[i+len(`"intended_usage":"`):]
		if j := strings.IndexByte(rest, '"'); j >= 0 {
			usages[rest[:j]]++
			rest = rest[j:]
		}
	}
	t.Logf("intended_usage 出现统计: %v", usages)
	for _, marker := range []string{"<think>", "</think>", "\"reasoning", "\"thinking", "\"cot", "chain_of_thought", "plan_block", "goal_block", "reasoning_plan"} {
		if strings.Contains(raw, marker) {
			t.Logf("raw 含标记 %q: 是", marker)
		}
	}
	content := answer.Content()
	head := content
	if len(head) > 500 {
		head = head[:500]
	}
	t.Logf("answer 前500字符:\n%s", head)
	t.Logf("answer 以 <think> 开头: %v；含 <think>: %v；总长: %d", strings.HasPrefix(strings.TrimSpace(content), "<think>"), strings.Contains(content, "<think>"), len(content))
	t.Logf("raw SSE 字节数(截断上限1MB): %d", len(raw))
}

func rateStr(n, total int) string {
	if total == 0 {
		return "n/a"
	}
	return strconv.Itoa(n) + "/" + strconv.Itoa(total)
}

type variantResult struct {
	status        int
	sourceCount   int
	stepCount     int
	adhered       bool
	answerPreview string
	rawPreview    string
	err           string
}

func runSearchFocusVariant(t *testing.T, client *http.Client, creds Credentials, target string, v searchFocusVariant) variantResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	body := buildProbeBody(probeQuery, probeModelPreference(v.mode), v.mode, v.searchFocus, v.sources, creds)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return variantResult{err: "build request: " + err.Error()}
	}
	creds.SetFetchStyleHeaders(req.Header, "text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		return variantResult{err: "do request: " + err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()

	res := variantResult{status: resp.StatusCode}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		res.rawPreview = string(raw)
		res.err = "non-200 status"
		return res
	}

	// tee 原始 SSE 到一个有上限的缓冲，便于判读；Decoder 复用生产解析逻辑。
	capped := &cappedBuffer{limit: 256 * 1024}
	decoder := NewDecoder(io.TeeReader(resp.Body, capped))
	var answer Answer
	var sources []Source
	for {
		if ctx.Err() != nil {
			res.err = "timeout/canceled before end_of_stream"
			break
		}
		ev, derr := decoder.Next()
		if derr != nil {
			if derr != io.EOF {
				res.err = "decode: " + derr.Error()
			}
			break
		}
		if len(ev.Sources) > 0 {
			sources = MergeSources(sources, ev.Sources)
		}
		res.stepCount += len(ev.Steps)
		if _, aerr := answer.Apply(ev, true); aerr != nil {
			res.err = "answer apply: " + aerr.Error()
			break
		}
		if ev.EndOfStream {
			break
		}
	}
	res.sourceCount = len(sources)
	res.answerPreview = answer.Content()
	res.rawPreview = capped.String()
	// 遵从判定：去掉可能的引用角标后，正文等于口令即算遵从。
	cleaned := strings.TrimSpace(StripCitationMarkers(answer.Content()))
	res.adhered = cleaned == probeSentinel
	return res
}

// buildProbeBody 复刻 BuildAskBodyWithOptions 的 payload 形状，但把 search_focus /
// mode / sources 开放为参数，以便逐一验证哪个杠杆能关闭联网搜索。
func buildProbeBody(query, modelPref, mode, searchFocus string, sources []string, creds Credentials) []byte {
	if sources == nil {
		sources = []string{}
	}
	payload := map[string]any{
		"query_str": query,
		"params": map[string]any{
			"attachments":           []string{},
			"frontend_context_uuid": uuid.NewString(),
			"frontend_uuid":         uuid.NewString(),
			"is_incognito":          false,
			"language":              "en-US",
			"timezone":              "UTC",
			"last_backend_uuid":     nil,
			"mode":                  mode,
			"model_preference":      modelPref,
			"source":                "default",
			"sources":               sources,
			"search_focus":          searchFocus,
			"version":               askVersion,
		},
	}
	data, _ := json.Marshal(payload)
	return data
}

// probeModelPreference 按 mode 选一个合理的默认 model_preference，PPLX_MODEL 可覆盖。
func probeModelPreference(mode string) string {
	if override := strings.TrimSpace(os.Getenv("PPLX_MODEL")); override != "" {
		return override
	}
	if mode == ModeCopilot {
		return "pplx_pro"
	}
	return "turbo"
}

// newProbeClient 构造一个使用生产同款 uTLS 指纹、禁用重定向的 HTTP 客户端。
// 默认 profile 的 ALPN 为 http/1.1，SSE 走 HTTP/1.1 即可。
func newProbeClient(proxy string) (*http.Client, error) {
	profile := &tlsfingerprint.Profile{Name: "perplexity-default"}
	var dialTLS func(ctx context.Context, network, addr string) (net.Conn, error)
	if proxy == "" {
		dialTLS = tlsfingerprint.NewDialer(profile, nil).DialTLSContext
	} else {
		u, err := url.Parse(proxy)
		if err != nil {
			return nil, err
		}
		if u.Scheme == "socks5" || u.Scheme == "socks5h" {
			dialTLS = tlsfingerprint.NewSOCKS5ProxyDialer(profile, u).DialTLSContext
		} else {
			dialTLS = tlsfingerprint.NewHTTPProxyDialer(profile, u).DialTLSContext
		}
	}
	transport := &http.Transport{
		DialTLSContext:      dialTLS,
		ForceAttemptHTTP2:   false,
		TLSHandshakeTimeout: 20 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		// 浏览器会话凭证绝不能被重定向到其它 origin。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// cappedBuffer 是一个写入上限的缓冲，防止大 SSE 占满内存。
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := c.limit - c.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			c.buf.Write(p[:remaining])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }
