package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const openAITimezoneDetectURL = "http://ip-api.com/json/?fields=status,message,country,regionName,city,timezone,query"

// DetectOpenAIRequestTimezone 通过账号代理探测出口 IP 及其时区，并把结果写回账号：
//   - extra.openai_request_timezone：后续请求改写使用的 IANA 时区
//   - extra.openai_request_timezone_detected：最近一次检测明细（含失败原因）
//
// 仅「跟随出口」模式使用；固定模式不会调用。
func (s *OpenAIGatewayService) DetectOpenAIRequestTimezone(ctx context.Context, account *Account) (OpenAIDetectedTimezone, error) {
	result := OpenAIDetectedTimezone{DetectedAt: time.Now().Format(time.RFC3339)}
	if s == nil || s.httpUpstream == nil || account == nil {
		return result, fmt.Errorf("timezone detection is unavailable")
	}
	proxyURL, err := account.ProxyURLForOutbound()
	if err != nil {
		result.Error = err.Error()
		_ = s.persistOpenAIRequestTimezone(ctx, account, result)
		return result, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, openAITimezoneDetectURL, nil)
	if err != nil {
		result.Error = err.Error()
		_ = s.persistOpenAIRequestTimezone(ctx, account, result)
		return result, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		result.Error = fmt.Sprintf("exit ip lookup failed: %v", err)
		_ = s.persistOpenAIRequestTimezone(ctx, account, result)
		return result, err
	}
	if resp == nil || resp.Body == nil {
		result.Error = "exit ip lookup returned no response"
		_ = s.persistOpenAIRequestTimezone(ctx, account, result)
		return result, fmt.Errorf("exit ip lookup returned no response")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		result.Error = fmt.Sprintf("read exit ip response: %v", err)
		return result, err
	}

	var payload struct {
		Status     string `json:"status"`
		Message    string `json:"message"`
		Country    string `json:"country"`
		RegionName string `json:"regionName"`
		City       string `json:"city"`
		Timezone   string `json:"timezone"`
		Query      string `json:"query"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		result.Error = fmt.Sprintf("decode exit ip response: %v", err)
		return result, err
	}
	result.IP = payload.Query
	result.Country = payload.Country
	result.Region = payload.RegionName
	result.City = payload.City
	if payload.Status != "success" {
		result.Error = strings.TrimSpace(payload.Message)
		if result.Error == "" {
			result.Error = "exit ip lookup failed"
		}
		_ = s.persistOpenAIRequestTimezone(ctx, account, result)
		return result, fmt.Errorf("%s", result.Error)
	}
	if !isValidOpenAIRequestTimezone(payload.Timezone) {
		result.Error = fmt.Sprintf("unsupported exit timezone %q", payload.Timezone)
		_ = s.persistOpenAIRequestTimezone(ctx, account, result)
		return result, fmt.Errorf("%s", result.Error)
	}
	result.Timezone = payload.Timezone
	if err := s.persistOpenAIRequestTimezone(ctx, account, result); err != nil {
		return result, err
	}
	return result, nil
}

func (s *OpenAIGatewayService) persistOpenAIRequestTimezone(ctx context.Context, account *Account, result OpenAIDetectedTimezone) error {
	if s == nil || s.accountRepo == nil || account == nil || account.ID <= 0 {
		return nil
	}
	updates := map[string]any{openAIRequestTimezoneDetectedExtraKey: result}
	if result.Timezone != "" && isValidOpenAIRequestTimezone(result.Timezone) {
		updates[openAIRequestTimezoneExtraKey] = result.Timezone
	}
	if existing, ok := account.Extra[openAIRequestTimezoneDetectedExtraKey].(OpenAIDetectedTimezone); ok && existing.Timezone == result.Timezone && existing.IP == result.IP && result.Error == "" {
		return nil
	}
	mergeAccountExtra(account, updates)
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		return err
	}
	return nil
}
