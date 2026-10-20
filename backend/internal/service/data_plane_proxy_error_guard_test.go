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

// proxyURLResolvers 是解析账号出站代理的函数（含各包装函数）。
var proxyURLResolvers = map[string]bool{
	"ProxyURLForOutbound":          true,
	"ProxyEgressIdentityURL":       true,
	"accountProxyURLWithRepo":      true,
	"resolveProxyURLByID":          true,
	"antigravityCompatProxyURL":    true,
	"resolveAccountProxyURL":       true,
	"upstreamModelsProxyURL":       true,
	"vertexServiceAccountProxyURL": true,
}

// proxyErrorConverters 把代理错误转成 failover（或按平台约定处理）的函数。
var proxyErrorConverters = map[string]bool{
	"handleOpenAIUpstreamTransportError":  true,
	"handleUpstreamTransportError":        true,
	"handleAccessTokenTransportError":     true,
	"antigravityProxyUnavailableFailover": true,
	"forwardNativeTransportFailure":       true,
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

func TestDataPlaneProxyErrorsAreConvertedToFailover(t *testing.T) {
	// 自检：检测器必须认出典型的原样返回写法，否则护栏形同虚设。
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
}`
	fset := token.NewFileSet()
	selfFile, err := parser.ParseFile(fset, "self.go", selfCheck, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"OpenAIGatewayService.a", "OpenAIGatewayService.b"}, rawProxyErrorReturns(selfFile))

	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	found := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		// 生产文件里也有 *_test_*.go（account_test_service.go 等），只排除 _test.go 后缀。
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		for _, fn := range rawProxyErrorReturns(file) {
			found[fn] = true
		}
	}

	var offenders, stale []string
	for fn := range found {
		if _, ok := rawProxyErrorAllowlist[fn]; !ok {
			offenders = append(offenders, fn)
		}
	}
	for fn := range rawProxyErrorAllowlist {
		if !found[fn] {
			stale = append(stale, fn)
		}
	}
	sort.Strings(offenders)
	sort.Strings(stale)
	require.Empty(t, offenders, "data-plane proxy resolution errors must go through the platform failover helper (e.g. handleOpenAIUpstreamTransportError); a raw ErrAccountProxyUnavailable turns a broken binding into a terminal error instead of an account switch")
	require.Empty(t, stale, "allowlist entries that no longer return the raw proxy error; remove them")
}

// rawProxyErrorReturns 返回数据面 receiver 的方法里“解析代理后在 if err != nil 分支原样返回
// （或仅包装后返回）该错误”的方法名（Receiver.Method）。
func rawProxyErrorReturns(file *ast.File) []string {
	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 || fn.Body == nil {
			continue
		}
		recv := receiverTypeName(fn.Recv.List[0].Type)
		if !dataPlaneProxyErrorReceivers[recv] {
			continue
		}
		raw := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			block, ok := n.(*ast.BlockStmt)
			if !ok {
				return true
			}
			for i, stmt := range block.List {
				ifStmt, ok := stmt.(*ast.IfStmt)
				if !ok {
					continue
				}
				errName := proxyResolverErrName(ifStmt.Init)
				if errName == "" && i > 0 {
					errName = proxyResolverErrName(block.List[i-1])
				}
				if errName == "" || !isNotNilCheck(ifStmt.Cond, errName) {
					continue
				}
				if returnsRawError(ifStmt.Body, errName) {
					raw = true
				}
			}
			return true
		})
		if raw {
			out = append(out, recv+"."+fn.Name.Name)
		}
	}
	sort.Strings(out)
	return out
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

// proxyResolverErrName：stmt 是 `x, err := <resolver>(...)` 时返回 err 的名字。
func proxyResolverErrName(stmt ast.Stmt) string {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || len(assign.Rhs) != 1 || len(assign.Lhs) == 0 {
		return ""
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok || !proxyURLResolvers[calleeName(call)] {
		return ""
	}
	ident, ok := assign.Lhs[len(assign.Lhs)-1].(*ast.Ident)
	if !ok || ident.Name == "_" {
		return ""
	}
	return ident.Name
}

func isNotNilCheck(cond ast.Expr, name string) bool {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	x, ok := bin.X.(*ast.Ident)
	y, ok2 := bin.Y.(*ast.Ident)
	return ok && ok2 && x.Name == name && y.Name == "nil"
}

// returnsRawError：body 里有 return 结果引用了 errName，且该结果不是对转换函数的调用。
func returnsRawError(body *ast.BlockStmt, errName string) bool {
	raw := false
	ast.Inspect(body, func(n ast.Node) bool {
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

func mentionsIdent(expr ast.Expr, name string) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok && ident.Name == name {
			found = true
		}
		return !found
	})
	return found
}
