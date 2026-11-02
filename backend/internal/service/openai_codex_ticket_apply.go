package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// 票据（x-codex-turn-state）复用：上游在响应头铸造的 turn-state 会被客户端在
// 同一回合里回带。中转站把它按账号缓存下来，后续请求主动注入，可以让网关
// 稳定命中满血线路（健康票据实测长度约 292，降智票据约 780）。
const (
	codexTurnTicketExtraKey      = "codex_turn_ticket"
	codexTurnTicketModelExtraKey = "codex_turn_ticket_model"
	codexTurnTicketAtExtraKey    = "codex_turn_ticket_at"

	codexTurnTicketMinHealthyLen = 120
	codexTurnTicketMaxHealthyLen = 480
	codexTurnTicketTTL           = 6 * time.Hour
)

// CodexTurnTicket 返回该账号针对 model 的可用票据。
func (a *Account) CodexTurnTicket(model string) (string, bool) {
	if a == nil || a.Extra == nil {
		return "", false
	}
	state, _ := a.Extra[codexTurnTicketExtraKey].(string)
	if state == "" {
		return "", false
	}
	ticketModel, _ := a.Extra[codexTurnTicketModelExtraKey].(string)
	if ticketModel != "" && model != "" && !strings.EqualFold(strings.TrimSpace(ticketModel), strings.TrimSpace(model)) {
		return "", false
	}
	rawAt, _ := a.Extra[codexTurnTicketAtExtraKey].(string)
	if rawAt != "" {
		at, err := time.Parse(time.RFC3339, rawAt)
		if err != nil || time.Since(at) > codexTurnTicketTTL {
			return "", false
		}
	}
	return state, true
}

// recordCodexTurnTicket 把上游返回的健康票据写回账号 extra（异步、节流）。
func (s *OpenAIGatewayService) recordCodexTurnTicket(account *Account, state, model string) {
	if s == nil || s.accountRepo == nil || account == nil || account.ID <= 0 {
		return
	}
	state = strings.TrimSpace(state)
	if len(state) < codexTurnTicketMinHealthyLen || len(state) > codexTurnTicketMaxHealthyLen {
		return
	}
	model = strings.TrimSpace(model)
	updates := map[string]any{
		codexTurnTicketExtraKey:      state,
		codexTurnTicketModelExtraKey: model,
		codexTurnTicketAtExtraKey:    time.Now().Format(time.RFC3339),
	}
	if existing, ok := account.Extra[codexTurnTicketExtraKey].(string); ok && existing == state {
		return
	}
	mergeAccountExtra(account, updates)
	go func(accountID int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.accountRepo.UpdateExtra(ctx, accountID, updates)
	}(account.ID)
}

// applyCodexTurnTicketHeader 在出站请求上注入票据；客户端自带的值会被覆盖，
// 因为客户端回带的是它自己上一回合的 blob，可能来自别的账号或已经过期。
func (s *OpenAIGatewayService) applyCodexTurnTicketHeader(header http.Header, account *Account, body []byte) {
	if header == nil || account == nil || !account.UsesOpenAICodexProtocol() {
		return
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	state, ok := account.CodexTurnTicket(model)
	if !ok {
		return
	}
	header.Set(openAICodexTurnStateHeader, state)
}
