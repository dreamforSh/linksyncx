//go:build unit

package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 数据面转发服务（这些 receiver 的方法）解析账号代理失败时，错误必须交给转换函数变成
// *UpstreamFailoverError：handler 只认它来换号，原样返回 ErrAccountProxyUnavailable 会让
// 绑定失效的账号把请求判死（违背 proxy-never-direct 的“数据面 fail-closed 且换号”）。
// 调用点有三十多处且还在增加，逐个写行为测试兜不住新增点，所以用 AST 护栏锁住写法。
var dataPlaneProxyErrorReceivers = map[string]bool{
	"OpenAIGatewayService":        true,
	"GeminiMessagesCompatService": true,
	"AntigravityGatewayService":   true,
	"GatewayService":              true,
}

// nonDataPlaneTransportReceivers 是自己发上游请求、但不在数据面换号循环里的 receiver，护栏不检查
// 它们的方法，每个都写明理由。凡是有方法调用 httpUpstream.Do / DoWithTLS、doOpenAIUpstream 或 Dial
// 的 receiver，都必须登记在 dataPlaneProxyErrorReceivers 或这里：新加的转发服务类型不会因为没人想起来
// 而落在护栏之外。
var nonDataPlaneTransportReceivers = map[string]string{
	"AccountTestService":          "账号测试：代理不可用的原因回给管理员，不换号",
	"CNProviderBalanceService":    "余额查询，不是转发",
	"CNProviderQuotaService":      "配额查询，不是转发",
	"EmailService":                "SMTP 发信，不经账号代理",
	"GrokQuotaService":            "配额探测，不是转发",
	"OllamaCloudUsageService":     "用量页抓取，不是转发",
	"OpenCodeGoUsageService":      "用量查询，不是转发",
	"UpstreamBillingProbeService": "计费探测，不是转发",
	"coderOpenAIWSClientDialer":   "WS 拨号器本身：代理 URL 由调用方解析后传入",
	"commandCodeUsageClient":      "用量查询客户端：代理 URL 由调用方解析后传入",
	"openAIWSConnPool":            "WS 连接池本身：代理 URL 由调用方解析后传入",
}

// proxyURLResolvers 是解析账号出站代理的函数（含各包装函数）：调用它们的数据面代码都要受检。
// 一行委托的包装函数（`return <resolver>(...)`）必须登记在这里，护栏才会检查它的调用方；
// 不登记的包装函数会让测试失败。这条不分 receiver：包装函数写在 Account 或控制面服务上，
// 数据面一样可能调用它（ProxyURLForOutbound 本身就在 Account 上）。
var proxyURLResolvers = map[string]bool{
	"ProxyURLForOutbound":          true,
	"ProxyEgressIdentityURL":       true,
	"accountProxyURLWithRepo":      true,
	"resolveProxyURLByID":          true,
	"antigravityCompatProxyURL":    true,
	"resolveAccountProxyURL":       true,
	"upstreamModelsProxyURL":       true,
	"vertexServiceAccountProxyURL": true,
	// AccountTestService 的方法，调用方都在账号测试里（不受检）；登记它只是为了让上面的规则没有例外。
	"grokTestProxyURL": true,
}

// primitiveProxyURLResolvers 是自己完成解析、不委托给其它解析函数的那几个；其余登记项必须确实是委托包装。
var primitiveProxyURLResolvers = map[string]bool{
	"ProxyURLForOutbound":    true,
	"ProxyEgressIdentityURL": true,
	"resolveProxyURLByID":    true,
}

// proxyErrorConverters 把代理错误转成 failover（或按平台约定处理）的函数。
var proxyErrorConverters = map[string]bool{
	"handleOpenAIUpstreamTransportError":   true,
	"handleOpenAIWSUpstreamTransportError": true,
	"handleUpstreamTransportError":         true,
	"handleAccessTokenTransportError":      true,
	"antigravityProxyUnavailableFailover":  true,
	"forwardNativeTransportFailure":        true,
}

// rawProxyErrorAllowlist 是可以直接返回代理错误的非换号路径，每条都写明理由。
var rawProxyErrorAllowlist = map[string]string{
	"GatewayService.ForwardCountTokens":                           "count_tokens 没有换号循环：先写 502 再返回",
	"GatewayService.forwardCountTokensAnthropicAPIKeyPassthrough": "同上",
	"OpenAIGatewayService.ForwardResponsesInputTokens":            "同上",
	"OpenAIGatewayService.ForwardCountTokensAsAnthropic":          "同上",
	"OpenAIGatewayService.FetchCodexModelsManifest":               "模型清单拉取，不是转发",
	"OpenAIGatewayService.FetchOpenAIModelsList":                  "模型列表拉取，不是转发",
	"OpenAIGatewayService.ResolvePluginOutboundIdentity":          "插件出站身份解析，不是转发",
	"OpenAIGatewayService.fetchOpenAIImageURLBase64":              "成功响应后的 b64 回填：失败只记日志并跳过",
	"OpenAIGatewayService.preparePerplexityImageAttachments":      "sendPerplexityCCUpstreamRequest 已先校验同一账号的绑定",
	"OpenAIGatewayService.createUpstreamLiveCall":                 "Live 建链循环对任何非 HTTP 错误都换号（shouldFailoverLiveCreateError）",
	"OpenAIGatewayService.dialLiveSideband":                       "已建立的 Live 通话固定在该账号，无号可换",
	"OpenAIGatewayService.OpenGrokRealtime":                       "handler 的候选循环遇到任何错误都换下一个账号",
	"OpenAIGatewayService.ProbeGrokRealtime":                      "握手探测，不是转发",
	"GeminiMessagesCompatService.ForwardAIStudioGET":              "模型列表 GET，handler 直接回 502",
	"AntigravityGatewayService.TestConnection":                    "账号测试，不是转发",
}

// rawProxyErrorHelpers 是解析代理后把错误原样交给调用方、自己却不是委托包装的包级函数（无 receiver）。
// 护栏看不到这类函数的调用方怎么处理返回的错误（doWebSearch 曾经就这样漏掉：Anthropic Web Search 模拟把
// 它的错误原样上抛，绑定失效的请求被判死），所以每个都要登记“谁来转换 / 为什么不需要换号”；新增的包级
// 函数必须先在这里说明清楚，或者把代理解析挪进数据面 receiver 的方法里、交给护栏检查。一行委托的包装函数
// 应登记进 proxyURLResolvers，让护栏去检查它的调用方。
var rawProxyErrorHelpers = map[string]string{
	"sendPerplexityAuthenticated":        "数据面调用方 sendPerplexityCCUpstreamRequest 先查绑定，发送错误交给 handleOpenAIUpstreamTransportError；上传授权前同一请求已查过绑定；其余调用方是探测 / 模型发现",
	"getVertexServiceAccountAccessToken": "取 token 步骤：Anthropic 入口先查绑定，取 token 的错误再经 handleAccessTokenTransportError 兜底；Gemini 入口先查绑定再取 token；批量生图是异步任务，没有换号循环",
	"registerAgentIdentityTask":          "Agent Identity 取凭证步骤：普通账号的转发入口已先查同一绑定；影子账号用父账号的绑定，父账号绑定失效时仍是终态（取凭证阶段的遗留问题，与 main 一致）",
}

func TestDataPlaneProxyErrorsAreConvertedToFailover(t *testing.T) {
	// 自检：检测器必须认出典型的原样返回写法（含判错不紧邻、复合条件、一行委托包装、case 体与
	// else if / switch 的 Init 里解析、var 声明、else if 链和无 tag switch 里判错、中间隔着 if-init 遮蔽），
	// 也不能把重新赋值后的别的错误算到代理解析头上，否则护栏形同虚设。
	selfCheck := `package service
func (s *OpenAIGatewayService) a() error {
	proxyURL, proxyErr := account.ProxyURLForOutbound()
	if proxyErr != nil {
		return nil, proxyErr
	}
	_ = proxyURL
	return nil
}
func (s *OpenAIGatewayService) b() error {
	if _, err := account.ProxyURLForOutbound(); err != nil {
		return nil, fmt.Errorf("x: %w", err)
	}
	return nil
}
func (s *OpenAIGatewayService) c() error {
	proxyURL, proxyErr := account.ProxyURLForOutbound()
	if proxyErr != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, proxyErr, false)
	}
	return nil
}
func (h *SomethingElse) d() error {
	proxyURL, proxyErr := account.ProxyURLForOutbound()
	if proxyErr != nil {
		return proxyErr
	}
	return nil
}
func e() error {
	proxyURL, err := resolveAccountProxyURL(account)
	if err != nil {
		return fmt.Errorf("x: %w", err)
	}
	return nil
}
func (s *GeminiMessagesCompatService) gap() error {
	proxyURL, proxyErr := account.ProxyURLForOutbound()
	upstreamStart := time.Now()
	if proxyErr != nil {
		return nil, proxyErr
	}
	return nil
}
func (s *GatewayService) compound() error {
	proxyURL, proxyErr := account.ProxyURLForOutbound()
	if proxyErr != nil && ctx.Err() == nil {
		return nil, proxyErr
	}
	return nil
}
func (s *GatewayService) either() error {
	proxyURL, proxyErr := account.ProxyURLForOutbound()
	if proxyURL == "" || proxyErr != nil {
		return nil, proxyErr
	}
	return nil
}
func (s *GatewayService) reassigned() error {
	proxyURL, err := account.ProxyURLForOutbound()
	if err != nil {
		return s.handleUpstreamTransportError(ctx, c, account, err, ev)
	}
	resp, err := s.httpUpstream.Do(req, proxyURL, 1, 1)
	if err != nil {
		return err
	}
	return nil
}
func (s *GatewayService) shadowed() error {
	proxyURL, err := account.ProxyURLForOutbound()
	err = prepare()
	if err != nil {
		return err
	}
	return nil
}
func pkgWrapper(a *Account) (string, error) {
	return a.ProxyURLForOutbound()
}
func (s *AntigravityGatewayService) methodWrapper(a *Account) (string, error) {
	return a.ProxyURLForOutbound()
}
func (h *SomethingElse) controlWrapper(a *Account) (string, error) {
	return a.ProxyURLForOutbound()
}
func (x *NewForwarder) send() {
	x.httpUpstream.DoWithTLS(req, "", 1, 1, nil)
}
func (s *OpenAIGatewayService) caseBody(x int) error {
	switch x {
	case 1:
		proxyURL, err := account.ProxyURLForOutbound()
		if err != nil {
			return nil, err
		}
	}
	return nil
}
func (s *OpenAIGatewayService) selectBody() error {
	select {
	case <-ch:
		proxyURL, err := account.ProxyURLForOutbound()
		if err != nil {
			return err
		}
	}
	return nil
}
func (s *GatewayService) ifInitShadow() error {
	proxyURL, err := account.ProxyURLForOutbound()
	if err := validate(); err != nil {
		return err
	}
	if err != nil {
		return nil, err
	}
	return nil
}
func (s *GatewayService) ifInitShadowConverted() error {
	proxyURL, err := account.ProxyURLForOutbound()
	if err := validate(); err != nil {
		return err
	}
	if err != nil {
		return s.handleUpstreamTransportError(ctx, c, account, err, ev)
	}
	return nil
}
func (s *GatewayService) ifInitReassigned() error {
	proxyURL, err := account.ProxyURLForOutbound()
	if err = validate(); err != nil {
		return err
	}
	if err != nil {
		return err
	}
	return nil
}
func (s *GeminiMessagesCompatService) elseIfInit() error {
	if skip {
		return nil
	} else if _, err := account.ProxyURLForOutbound(); err != nil {
		return err
	}
	return nil
}
func (s *GeminiMessagesCompatService) elseIfCond() error {
	proxyURL, err := account.ProxyURLForOutbound()
	if skip {
		return nil
	} else if err != nil {
		return err
	}
	return nil
}
func (s *AntigravityGatewayService) varDecl() error {
	var proxyURL, err = account.ProxyURLForOutbound()
	if err != nil {
		return err
	}
	return nil
}
func (s *OpenAIGatewayService) switchCase() error {
	proxyURL, err := account.ProxyURLForOutbound()
	switch {
	case proxyURL == "":
	case err != nil:
		return nil, err
	}
	return nil
}
func (s *OpenAIGatewayService) switchInit() error {
	switch _, err := account.ProxyURLForOutbound(); {
	case err != nil:
		return err
	}
	return nil
}
func (s *OpenAIGatewayService) switchConverted() error {
	proxyURL, err := account.ProxyURLForOutbound()
	switch {
	case err != nil:
		return s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	if ctx.Err() != nil || err != nil {
		return err
	}
	resp, err := s.httpUpstream.Do(req, proxyURL, 1, 1)
	if err != nil {
		return err
	}
	return nil
}
func (s *GatewayService) ifConverted() error {
	proxyURL, err := account.ProxyURLForOutbound()
	if err != nil {
		return s.handleUpstreamTransportError(ctx, c, account, err, ev)
	}
	if ctx.Err() != nil || err != nil {
		return err
	}
	return nil
}
func (a *Account) accountWrapper() (string, error) {
	return a.ProxyURLForOutbound()
}`
	fset := token.NewFileSet()
	selfFile, err := parser.ParseFile(fset, "self.go", selfCheck, 0)
	require.NoError(t, err)
	self := scanProxyErrorGuard([]*ast.File{selfFile})
	require.Equal(t, []string{
		"AntigravityGatewayService.varDecl",
		"GatewayService.compound",
		"GatewayService.either",
		"GatewayService.ifInitShadow",
		"GeminiMessagesCompatService.elseIfCond",
		"GeminiMessagesCompatService.elseIfInit",
		"GeminiMessagesCompatService.gap",
		"OpenAIGatewayService.a",
		"OpenAIGatewayService.b",
		"OpenAIGatewayService.caseBody",
		"OpenAIGatewayService.selectBody",
		"OpenAIGatewayService.switchCase",
		"OpenAIGatewayService.switchInit",
		"e",
	}, proxyGuardSortedKeys(self.raw))
	// 委托包装不分 receiver 都要登记：控制面服务和 Account 上的包装一样会被数据面调用。
	require.Equal(t, []string{
		"Account.accountWrapper",
		"AntigravityGatewayService.methodWrapper",
		"SomethingElse.controlWrapper",
		"pkgWrapper",
	}, proxyGuardSortedKeys(self.wrappers))
	require.Equal(t, []string{"GatewayService", "NewForwarder", "OpenAIGatewayService"}, proxyGuardSortedKeys(self.transportReceivers))

	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		// 生产文件里也有 *_test_*.go（account_test_service.go 等），只排除 _test.go 后缀。
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		files = append(files, file)
	}
	scan := scanProxyErrorGuard(files)

	var offenders, unregisteredWrappers, stale, staleResolvers, unclassifiedReceivers, staleReceivers []string
	for fn := range scan.raw {
		_, allowed := rawProxyErrorAllowlist[fn]
		_, helper := rawProxyErrorHelpers[fn]
		if !allowed && !helper {
			offenders = append(offenders, fn)
		}
	}
	for fn, bare := range scan.wrappers {
		_, helper := rawProxyErrorHelpers[fn]
		if !proxyURLResolvers[bare] && !helper {
			unregisteredWrappers = append(unregisteredWrappers, fn)
		}
	}
	for _, accounted := range []map[string]string{rawProxyErrorAllowlist, rawProxyErrorHelpers} {
		for fn := range accounted {
			if !scan.raw[fn] && scan.wrappers[fn] == "" {
				stale = append(stale, fn)
			}
		}
	}
	delegating := map[string]bool{}
	for _, bare := range scan.wrappers {
		delegating[bare] = true
	}
	for name := range proxyURLResolvers {
		if !scan.declared[name] || (!primitiveProxyURLResolvers[name] && !delegating[name]) {
			staleResolvers = append(staleResolvers, name)
		}
	}
	for recv := range scan.transportReceivers {
		if _, ok := nonDataPlaneTransportReceivers[recv]; !ok && !dataPlaneProxyErrorReceivers[recv] {
			unclassifiedReceivers = append(unclassifiedReceivers, recv)
		}
	}
	for recv := range dataPlaneProxyErrorReceivers {
		if !scan.transportReceivers[recv] {
			staleReceivers = append(staleReceivers, recv)
		}
	}
	for recv := range nonDataPlaneTransportReceivers {
		if !scan.transportReceivers[recv] {
			staleReceivers = append(staleReceivers, recv)
		}
	}
	for _, list := range [][]string{offenders, unregisteredWrappers, stale, staleResolvers, unclassifiedReceivers, staleReceivers} {
		sort.Strings(list)
	}
	assert.Empty(t, offenders, "data-plane proxy resolution errors must go through the platform failover helper (e.g. handleOpenAIUpstreamTransportError); a raw ErrAccountProxyUnavailable turns a broken binding into a terminal error instead of an account switch. Package-level helpers must be listed in rawProxyErrorHelpers with the caller that converts the error")
	assert.Empty(t, unregisteredWrappers, "functions that return a proxy resolver's result directly are resolvers too: list them in proxyURLResolvers so their callers are checked (or in rawProxyErrorHelpers with the caller that converts the error)")
	assert.Empty(t, stale, "allowlist entries that no longer return the raw proxy error; remove them")
	assert.Empty(t, staleResolvers, "proxyURLResolvers entries must exist, and every non-primitive entry must still delegate with return <resolver>(...)")
	assert.Empty(t, unclassifiedReceivers, "a receiver that sends upstream requests must be classified: add it to dataPlaneProxyErrorReceivers if it forwards client traffic, else to nonDataPlaneTransportReceivers with the reason")
	assert.Empty(t, staleReceivers, "receiver classifications that no longer send upstream requests; remove them")
}

type proxyErrorGuardScan struct {
	// raw：在受检范围内解析代理后原样（或仅包装后）返回该错误的函数，数据面 receiver 的方法记为
	// Receiver.Method，包级函数记为函数名；其它 receiver（账号测试、OAuth、用量查询等控制面服务）不检查。
	raw map[string]bool
	// wrappers：直接 return <resolver>(...) 的函数（不限 receiver），限定名 -> 裸名（调用点只看得到裸名）。
	// 也用于检查 proxyURLResolvers 里的包装登记是否过期。
	wrappers map[string]string
	// declared：包内声明的全部函数与方法的裸名。
	declared map[string]bool
	// transportReceivers：有方法直接发上游请求的 receiver。
	transportReceivers map[string]bool
}

func scanProxyErrorGuard(files []*ast.File) proxyErrorGuardScan {
	scan := proxyErrorGuardScan{
		raw:                map[string]bool{},
		wrappers:           map[string]string{},
		declared:           map[string]bool{},
		transportReceivers: map[string]bool{},
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			bare := fn.Name.Name
			scan.declared[bare] = true
			name, checked := bare, true
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				recv := receiverTypeName(fn.Recv.List[0].Type)
				if callsUpstreamTransport(fn.Body) {
					scan.transportReceivers[recv] = true
				}
				name, checked = recv+"."+bare, dataPlaneProxyErrorReceivers[recv]
			}
			if returnsResolverDirectly(fn.Body) {
				scan.wrappers[name] = bare
			}
			if checked && returnsRawProxyError(fn.Body) {
				scan.raw[name] = true
			}
		}
	}
	return scan
}

// returnsRawProxyError：函数体里解析代理之后，判这个错误的分支原样（或仅包装后）返回了它。
//   - 解析可以写成 `x, err := r()` / `x, err = r()` / `var x, err = r()`，出现在任意语句列表里（块、switch 与
//     select 的 case 体），也可以写在 if / else if / switch 的 Init 里；
//   - 判错可以不紧挨着解析（中间隔着别的语句），可以是 && / || 组合，可以是 else if 链里的一环，也可以是
//     无 tag switch 的 case（`case err != nil:` 是本仓库常见写法）或 `switch err`；
//   - 错误变量被重新赋值（=，或同一作用域的 :=）后再判的就是别的错误了，不再追踪；`if err := f(); ...`
//     只是在那个 if 里遮蔽，外层错误跳过这个 if 继续追踪。
//
// 只看受“提到该错误的条件”控制的分支里的 return；不判错就直接 return err 的写法不在检查范围内。
func returnsRawProxyError(body *ast.BlockStmt) bool {
	raw := false
	ast.Inspect(body, func(n ast.Node) bool {
		var r bool
		switch node := n.(type) {
		case *ast.BlockStmt:
			r = stmtListReturnsRawProxyError(node.List)
		case *ast.CaseClause:
			r = stmtListReturnsRawProxyError(node.Body)
		case *ast.CommClause:
			r = stmtListReturnsRawProxyError(node.Body)
		case *ast.IfStmt:
			// Init 里解析（含 else if 的 Init，Inspect 会走到链上每个 if）：错误只在这条 if 链里可见。
			if errName := proxyResolverErrName(node.Init); errName != "" {
				r, _ = ifChainReturnsRawError(node, errName, true)
			}
		case *ast.SwitchStmt:
			if errName := proxyResolverErrName(node.Init); errName != "" {
				r, _ = switchReturnsRawError(node, errName, true)
			}
		}
		raw = raw || r
		return true
	})
	return raw
}

// stmtListReturnsRawProxyError：语句列表里每个解析赋值，沿其后的语句找到处理点并检查。
func stmtListReturnsRawProxyError(list []ast.Stmt) bool {
	raw := false
	for i, stmt := range list {
		errName := proxyResolverErrName(stmt)
		if errName == "" {
			continue
		}
		for _, next := range list[i+1:] {
			var r, handled bool
			switch s := next.(type) {
			case *ast.IfStmt:
				r, handled = ifChainReturnsRawError(s, errName, false)
			case *ast.SwitchStmt:
				r, handled = switchReturnsRawError(s, errName, false)
			default:
				handled = reassignsIdent(next, errName)
			}
			raw = raw || r
			// 第一个含 err != nil 的判错就是处理点；之后的判断可能已经在判别的错误。
			if handled {
				break
			}
		}
	}
	return raw
}

// ifChainReturnsRawError 沿 if / else if 链找第一个条件提到 errName 的分支，返回从它起（含其后的整条链）
// 是否原样返回了错误，以及这里是不是处理点（条件含 errName != nil，或在链里重新赋了值）。链上某个 Init
// 用 := 声明了同名变量时，其后的分支看到的是新变量：停下但不算处理点，外层继续往后追踪。ownInit 表示
// 链首的 Init 就是这次解析本身。
func ifChainReturnsRawError(ifStmt *ast.IfStmt, errName string, ownInit bool) (raw, handled bool) {
	for cur := ifStmt; cur != nil; {
		if cur.Init != nil && (cur != ifStmt || !ownInit) {
			if definesIdent(cur.Init, errName) {
				return false, false
			}
			if reassignsIdent(cur.Init, errName) {
				return false, true
			}
		}
		if mentionsIdent(cur.Cond, errName) {
			return ifReturnsRawError(cur, errName), containsNotNilCheck(cur.Cond, errName) || reassignsIdent(cur, errName)
		}
		cur, _ = cur.Else.(*ast.IfStmt)
	}
	return false, false
}

// switchReturnsRawError：tag 提到 errName（`switch err`），或某个 case 表达式提到它时，任一 case 体原样
// 返回它都算（case 互斥，排在判错前面的 case 返回的也是可能非 nil 的同一个错误）。tag 提到错误、case 里有
// errName != nil、或在 switch 里重新赋了值时，这里就是处理点。Init 的遮蔽 / 重新赋值规则同 if。
func switchReturnsRawError(sw *ast.SwitchStmt, errName string, ownInit bool) (raw, handled bool) {
	if sw.Init != nil && !ownInit {
		if definesIdent(sw.Init, errName) {
			return false, false
		}
		if reassignsIdent(sw.Init, errName) {
			return false, true
		}
	}
	tagged := sw.Tag != nil && mentionsIdent(sw.Tag, errName)
	mentioned, notNil := tagged, false
	for _, stmt := range sw.Body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, expr := range clause.List {
			if mentionsIdent(expr, errName) {
				mentioned = true
				notNil = notNil || containsNotNilCheck(expr, errName)
			}
		}
	}
	if !mentioned {
		return false, false
	}
	return returnsRawError(sw.Body, errName), tagged || notNil || reassignsIdent(sw, errName)
}

// returnsResolverDirectly：函数体里有 `return <resolver>(...)`（不算闭包里的），即一行委托的包装函数。
func returnsResolverDirectly(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			for _, result := range node.Results {
				if call, ok := result.(*ast.CallExpr); ok && proxyURLResolvers[calleeName(call)] {
					found = true
				}
			}
		}
		return true
	})
	return found
}

// callsUpstreamTransport：函数体里直接发上游请求（httpUpstream.Do / DoWithTLS、doOpenAIUpstream、Dial）。
func callsUpstreamTransport(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			found = fun.Name == "doOpenAIUpstream"
		case *ast.SelectorExpr:
			switch fun.Sel.Name {
			case "doOpenAIUpstream", "Dial":
				found = true
			case "Do", "DoWithTLS":
				found = selectorEndsWith(fun.X, "httpUpstream")
			}
		}
		return !found
	})
	return found
}

func selectorEndsWith(expr ast.Expr, name string) bool {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name == name
	case *ast.SelectorExpr:
		return x.Sel.Name == name
	}
	return false
}

func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

func calleeName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	}
	return ""
}

// proxyResolverErrName：stmt 是 `x, err := <resolver>(...)`（或 =、或 `var x, err = <resolver>(...)`）时
// 返回 err 的名字。
func proxyResolverErrName(stmt ast.Stmt) string {
	switch s := stmt.(type) {
	case *ast.AssignStmt:
		if len(s.Rhs) == 1 && len(s.Lhs) > 0 && isProxyResolverCall(s.Rhs[0]) {
			if ident, ok := s.Lhs[len(s.Lhs)-1].(*ast.Ident); ok && ident.Name != "_" {
				return ident.Name
			}
		}
	case *ast.DeclStmt:
		gen, ok := s.Decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			return ""
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if ok && len(vs.Values) == 1 && len(vs.Names) > 0 && isProxyResolverCall(vs.Values[0]) {
				if name := vs.Names[len(vs.Names)-1].Name; name != "_" {
					return name
				}
			}
		}
	}
	return ""
}

func isProxyResolverCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	return ok && proxyURLResolvers[calleeName(call)]
}

// definesIdent：语句用 := 声明了 name。用在 if / switch 的 Init 上：那里的 := 总是新变量（遮蔽外层），
// 不是重新赋值。
func definesIdent(stmt ast.Stmt, name string) bool {
	assign, ok := stmt.(*ast.AssignStmt)
	return ok && assign.Tok == token.DEFINE && assignsTo(assign, name)
}

// containsNotNilCheck：条件里有 `name != nil` 子表达式（可以在 && / || 组合里）。
func containsNotNilCheck(cond ast.Expr, name string) bool {
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		bin, ok := n.(*ast.BinaryExpr)
		if !ok || found {
			return !found
		}
		if bin.Op == token.NEQ {
			x, ok := bin.X.(*ast.Ident)
			y, ok2 := bin.Y.(*ast.Ident)
			found = ok && ok2 && x.Name == name && y.Name == "nil"
		}
		return !found
	})
	return found
}

// reassignsIdent：语句给 name 重新赋了值。同层的 := / = 都算；嵌套块里只算 =（嵌套的 := 是新变量）。
func reassignsIdent(stmt ast.Stmt, name string) bool {
	if assign, ok := stmt.(*ast.AssignStmt); ok && assignsTo(assign, name) {
		return true
	}
	if decl, ok := stmt.(*ast.DeclStmt); ok {
		if gen, ok := decl.Decl.(*ast.GenDecl); ok {
			for _, spec := range gen.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					for _, ident := range vs.Names {
						if ident.Name == name {
							return true
						}
					}
				}
			}
		}
	}
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found {
			return false
		}
		if assign, ok := n.(*ast.AssignStmt); ok && assign.Tok == token.ASSIGN && assignsTo(assign, name) {
			found = true
		}
		return !found
	})
	return found
}

func assignsTo(assign *ast.AssignStmt, name string) bool {
	for _, lhs := range assign.Lhs {
		if ident, ok := lhs.(*ast.Ident); ok && ident.Name == name {
			return true
		}
	}
	return false
}

// ifReturnsRawError：if 的分支（含 else）里有 return 结果引用了 errName，且该结果不是对转换函数的调用。
func ifReturnsRawError(ifStmt *ast.IfStmt, errName string) bool {
	if returnsRawError(ifStmt.Body, errName) {
		return true
	}
	return ifStmt.Else != nil && returnsRawError(ifStmt.Else, errName)
}

// returnsRawError：node 里有 return 结果引用了 errName，且该结果不是对转换函数的调用。
func returnsRawError(node ast.Node, errName string) bool {
	raw := false
	ast.Inspect(node, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, result := range ret.Results {
			if call, ok := result.(*ast.CallExpr); ok && proxyErrorConverters[calleeName(call)] {
				continue
			}
			if mentionsIdent(result, errName) {
				raw = true
			}
		}
		return true
	})
	return raw
}

func mentionsIdent(expr ast.Node, name string) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok && ident.Name == name {
			found = true
		}
		return !found
	})
	return found
}

func proxyGuardSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
