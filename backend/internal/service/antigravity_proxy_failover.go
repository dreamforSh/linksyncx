package service

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// antigravityProxyUnavailableFailover 把 Antigravity 转发路径在出站前发现的代理绑定失效
// （ProxyURLForOutbound 返回的 ErrAccountProxyUnavailable：关系缺失 / 与 ProxyID 错配 /
// URL 为空或无效）转成账号不可用的 failover 错误。
//
// 为什么单独一个函数：Antigravity 的传输层错误不走 failover（重试后以终态错误结束），没有其它
// 平台那样的“传输错误 → failover”辅助函数；而绑定失效是确定性的（同一账号每次都失败），原样
// 返回只会让 handler 判死请求，违背“代理故障 fail-closed 且数据面换号”的约定。
//   - 记录 Ops request_error 事件（带代理归因），管理员能在错误日志里看到坏的是哪个绑定；
//   - 返回 502 *UpstreamFailoverError 且 AccountUnavailable=true：handler 换号，已绑定的
//     粘性会话也换号（IsAntigravityStickySoftFailure 把它视为硬性原因）；
//   - 不写响应：响应归 handler（换号，或耗尽后按端点格式渲染）；
//   - 不临时摘号：请求从未出站，绑定修好后应立即恢复调度。
//
// body 是该端点协议格式的错误体，供 handler 换号耗尽后做透传规则匹配与错误信息提取。
func antigravityProxyUnavailableFailover(c *gin.Context, account *Account, err error, body []byte) error {
	safeErr := sanitizeUpstreamErrorMessage(err.Error())
	setOpsUpstreamError(c, 0, safeErr, "")
	event := OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: 0,
		Kind:               "request_error",
		Message:            safeErr,
	}
	event.ProxyID, event.ProxyName = opsUpstreamProxyAttribution(account)
	appendOpsUpstreamError(c, event)
	return &UpstreamFailoverError{
		StatusCode:         http.StatusBadGateway,
		ResponseBody:       body,
		AccountUnavailable: true,
	}
}
