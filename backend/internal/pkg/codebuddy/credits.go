package codebuddy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ResourceProductCode    = "p_tcaca"
	ResourcePath           = "/v2/billing/meter/get-user-resource"
	EnterpriseUsagePath    = "/v2/billing/meter/get-enterprise-user-usage"
	EnterpriseAccountsPath = "/console/accounts"
	creditsPageSize        = 100
	creditsMaxPages        = 20
	creditsFetchAttempts   = 3
	billingTimeLayout      = "2006-01-02 15:04:05"
	// SegmentLabelUnknown is the locale-neutral label for unnamed packages.
	SegmentLabelUnknown = "credits"
)

// CreditSegment is one credit package.
type CreditSegment struct {
	Remaining   float64  `json:"remaining"`
	Total       float64  `json:"total"`
	ExpiresAt   *float64 `json:"expires_at,omitempty"`
	Source      string   `json:"source,omitempty"`
	PackageCode string   `json:"package_code,omitempty"`
}

// CreditsSnapshot is the account's balance as last probed.
type CreditsSnapshot struct {
	Credits       float64         `json:"credits"`
	Total         float64         `json:"total,omitempty"`
	Count         int             `json:"count"`
	Segments      []CreditSegment `json:"segments,omitempty"`
	SoonestExpiry *float64        `json:"soonest_expiry,omitempty"`
	Intl          bool            `json:"intl"`
	// Enterprise snapshots carry one cycle allowance instead of packages.
	Enterprise bool     `json:"enterprise,omitempty"`
	Unlimited  bool     `json:"unlimited,omitempty"`
	Used       float64  `json:"used,omitempty"`
	ResetAt    *float64 `json:"reset_at,omitempty"`
	FetchedAt  int64    `json:"fetched_at"`
	// Estimated: Deduct drew the balance down locally since the last probe.
	Estimated bool `json:"estimated,omitempty"`
	// Exhausted: the upstream rejected a request for lack of credits.
	Exhausted bool `json:"exhausted,omitempty"`
}

// Deduct draws a locally observed charge out of the cached balance, draining
// the soonest-expiring packages first like CodeBuddy does.
func (s *CreditsSnapshot) Deduct(amount float64) {
	if s == nil || amount <= 0 || s.Unlimited {
		return
	}
	if s.Enterprise {
		s.Used = round2(s.Used + amount)
		s.Credits = round2(math.Max(0, s.Credits-amount))
		s.Estimated = true
		return
	}
	order := make([]int, 0, len(s.Segments))
	for i := range s.Segments {
		order = append(order, i)
	}
	sort.SliceStable(order, func(a, b int) bool {
		return expiryRank(s.Segments[order[a]]) < expiryRank(s.Segments[order[b]])
	})
	remaining := amount
	for _, index := range order {
		if remaining <= 0 {
			break
		}
		segment := &s.Segments[index]
		if segment.Remaining <= 0 {
			continue
		}
		taken := math.Min(segment.Remaining, remaining)
		segment.Remaining = round2(segment.Remaining - taken)
		remaining -= taken
	}
	if len(s.Segments) == 0 {
		s.Credits = round2(math.Max(0, s.Credits-amount))
	} else {
		total := 0.0
		for _, segment := range s.Segments {
			total += segment.Remaining
		}
		s.Credits = round2(total)
	}
	s.SoonestExpiry = soonestExpiry(s.Segments, time.Now().Unix())
	s.Estimated = true
}

func expiryRank(segment CreditSegment) float64 {
	if segment.ExpiresAt == nil {
		return math.MaxFloat64
	}
	return *segment.ExpiresAt
}

// FetchCredits probes the balance on the profile's billing host, using the
// enterprise allowance endpoint for tenant accounts.
func FetchCredits(ctx context.Context, client HTTPDoer, creds Credentials) (*CreditsSnapshot, error) {
	host := BillingHost(creds.Profile)
	if creds.IsEnterprise() {
		if strings.TrimSpace(creds.EnterpriseID) == "" {
			id, err := ResolveEnterpriseID(ctx, client, creds)
			if err != nil {
				return nil, err
			}
			creds.EnterpriseID = id
		}
		return fetchEnterpriseCredits(ctx, client, host, creds)
	}
	headers := WebHeaders(host, IdentityOf(creds))
	var accounts []map[string]any
	for page := 1; page <= creditsMaxPages; page++ {
		pageAccounts, err := fetchResourcePage(ctx, client, host+ResourcePath, headers, page, page == 1)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, pageAccounts...)
		if len(pageAccounts) < creditsPageSize {
			break
		}
	}
	segments := mergeSegments(extractSegments(accounts))
	snapshot := &CreditsSnapshot{
		Count:         len(accounts),
		Segments:      segments,
		SoonestExpiry: soonestExpiry(segments, time.Now().Unix()),
		Intl:          IsInternational(creds.Profile),
		FetchedAt:     time.Now().Unix(),
	}
	for _, segment := range segments {
		snapshot.Credits += segment.Remaining
		snapshot.Total += segment.Total
	}
	snapshot.Credits = round2(snapshot.Credits)
	snapshot.Total = round2(snapshot.Total)
	return snapshot, nil
}

// fetchResourcePage retries transient failures; an empty first page is
// retried too because the endpoint intermittently answers 200 with no
// accounts, indistinguishable from a real empty balance otherwise.
func fetchResourcePage(ctx context.Context, client HTTPDoer, url string, headers map[string]string, page int, retryEmpty bool) ([]map[string]any, error) {
	var lastErr error
	for attempt := 0; attempt < creditsFetchAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 300 * time.Millisecond):
			}
		}
		raw, err := postBillingJSON(ctx, client, url, headers, resourceBody(page, time.Now()))
		if err != nil {
			if errors.Is(err, ErrAuthExpired) {
				return nil, err
			}
			lastErr = err
			continue
		}
		var root map[string]any
		if err := json.Unmarshal(raw, &root); err != nil {
			lastErr = fmt.Errorf("decode credits: %w", err)
			continue
		}
		if code, ok := root["code"]; ok && code != nil && fmt.Sprint(code) != "0" {
			msg, _ := root["msg"].(string)
			return nil, fmt.Errorf("credits code %v: %s", code, msg)
		}
		accounts, found := extractAccounts(root)
		if !found {
			lastErr = fmt.Errorf("credits response is missing the Accounts structure")
			continue
		}
		if len(accounts) == 0 && retryEmpty && attempt < creditsFetchAttempts-1 {
			lastErr = fmt.Errorf("credits response carried no accounts")
			continue
		}
		return accounts, nil
	}
	return nil, fmt.Errorf("query credits: %w", lastErr)
}

func resourceBody(page int, now time.Time) map[string]any {
	now = now.In(billingLocation())
	return map[string]any{
		"PageNumber":               page,
		"PageSize":                 creditsPageSize,
		"ProductCode":              ResourceProductCode,
		"Status":                   []int{0, 3},
		"PackageEndTimeRangeBegin": now.Format(billingTimeLayout),
		"PackageEndTimeRangeEnd":   now.Add(101 * 365 * 24 * time.Hour).Format(billingTimeLayout),
	}
}

func fetchEnterpriseCredits(ctx context.Context, client HTTPDoer, host string, creds Credentials) (*CreditsSnapshot, error) {
	raw, err := postBillingJSON(ctx, client, host+EnterpriseUsagePath, WebHeaders(host, IdentityOf(creds)), map[string]any{})
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode enterprise credits: %w", err)
	}
	if code, ok := payload["code"]; ok && code != nil {
		if c := fmt.Sprint(code); c != "0" && c != "200" {
			msg, _ := payload["msg"].(string)
			return nil, fmt.Errorf("enterprise credits code %s: %s", c, msg)
		}
	}
	snapshot, ok := ParseEnterpriseUsage(payload)
	if !ok {
		return nil, fmt.Errorf("enterprise allowance payload is unreadable")
	}
	snapshot.Intl = IsInternational(creds.Profile)
	snapshot.FetchedAt = time.Now().Unix()
	return snapshot, nil
}

// ParseEnterpriseUsage reads {limitNum, credit (used), cycleResetTime} from
// data.data, data or the top level. limitNum -1 means unlimited.
func ParseEnterpriseUsage(payload map[string]any) (*CreditsSnapshot, bool) {
	data, _ := payload["data"].(map[string]any)
	nested, _ := data["data"].(map[string]any)
	for _, candidate := range []map[string]any{nested, data, payload} {
		if len(candidate) == 0 {
			continue
		}
		limit, ok := firstNumber(candidate, []string{"limitNum", "limit_num"})
		if !ok || math.IsInf(limit, 0) || math.IsNaN(limit) {
			continue
		}
		used, _ := firstNumber(candidate, []string{"credit", "used_num"})
		used = math.Max(used, 0)
		snapshot := &CreditsSnapshot{
			Enterprise: true,
			Count:      1,
			Used:       round2(used),
			ResetAt:    firstTimestamp(candidate, []string{"cycleResetTime", "CycleResetTime", "cycle_reset_time"}),
		}
		if limit < 0 {
			snapshot.Unlimited = true
			return snapshot, true
		}
		snapshot.Total = round2(limit)
		snapshot.Credits = round2(math.Max(0, limit-used))
		return snapshot, true
	}
	return nil, false
}

// ResolveEnterpriseID looks up the tenant id for credentials that only carry
// an enterprise account type.
func ResolveEnterpriseID(ctx context.Context, client HTTPDoer, creds Credentials) (string, error) {
	host := BillingHost(creds.Profile)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+EnterpriseAccountsPath, nil)
	if err != nil {
		return "", err
	}
	ApplyHeaders(req.Header, WebHeaders(host, IdentityOf(creds)))
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return "", ErrAuthExpired
	}
	var payload struct {
		Code any            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &payload) != nil {
		return "", fmt.Errorf("enterprise tenant lookup failed (HTTP %d)", resp.StatusCode)
	}
	for _, key := range []string{"accounts", "Accounts"} {
		if items, ok := payload.Data[key].([]any); ok && len(items) > 0 {
			if first, ok := items[0].(map[string]any); ok {
				if id := stringField(first, "enterpriseId", KeyEnterpriseID); id != "" {
					return id, nil
				}
			}
		}
	}
	return "", fmt.Errorf("enterprise tenant id not found")
}

// postBillingJSON posts to a billing endpoint, mapping 401 to ErrAuthExpired.
func postBillingJSON(ctx context.Context, client HTTPDoer, url string, headers map[string]string, body map[string]any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	ApplyHeaders(req.Header, headers)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrAuthExpired
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("billing HTTP %d", resp.StatusCode)
	}
	return raw, nil
}

func extractAccounts(root map[string]any) ([]map[string]any, bool) {
	data, _ := root["data"].(map[string]any)
	candidates := []any{
		lookup(data, "Response", "Data", "Accounts"),
		lookup(data, "data", "Response", "Data", "Accounts"),
		lookup(data, "Accounts"),
		lookup(data, "accounts"),
	}
	for _, candidate := range candidates {
		raw, ok := candidate.([]any)
		if !ok {
			continue
		}
		out := make([]map[string]any, 0, len(raw))
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out, true
	}
	return nil, false
}

func lookup(v any, keys ...string) any {
	cur := v
	for _, key := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[key]
	}
	return cur
}

var remainingFields = []string{
	"SlicePeriodCapacityRemainPrecise", "SlicePeriodCapacityRemain",
	"CycleCapacityRemainPrecise", "CycleCapacityRemain",
	"CapacityRemainPrecise", "CapacityRemain",
	"RemainPrecise", "Remain", "Remaining", "Balance",
}
var totalFields = []string{
	"SlicePeriodCapacitySizePrecise", "SlicePeriodCapacitySize",
	"CycleCapacitySizePrecise", "CycleCapacitySize",
	"CycleCapacityPrecise", "CycleCapacity",
	"CapacityPrecise", "Capacity", "TotalCapacityPrecise", "TotalCapacity",
	"PackageCapacity", "Quota", "Amount",
}
var expiryFields = []string{
	"DeductionEndTime", "ExpiredTime", "SlicePeriodEndTime", "PackageEndTime",
	"EndTime", "CycleEndTime", "ExpireTime", "ExpirationTime",
	"ValidEndTime", "ValidPeriodEndTime", "EndAt", "ExpireAt",
}
var labelFields = []string{
	"PackageName", "PackageTypeName", "AccountName", "ProductName",
	"Name", "RuleName", "Description",
}

func extractSegments(accounts []map[string]any) []CreditSegment {
	out := make([]CreditSegment, 0)
	for _, account := range accounts {
		items := []map[string]any{account}
		if details, ok := account["SlicePeriodUsageDetails"].([]any); ok && len(details) > 0 {
			items = items[:0]
			for _, detail := range details {
				merged := map[string]any{}
				for k, v := range account {
					merged[k] = v
				}
				if dm, ok := detail.(map[string]any); ok {
					for k, v := range dm {
						merged[k] = v
					}
				}
				items = append(items, merged)
			}
		}
		for _, item := range items {
			remaining, ok := firstNumber(item, remainingFields)
			if !ok || remaining <= 0 {
				continue
			}
			total, hasTotal := firstNumber(item, totalFields)
			if !hasTotal || total < remaining {
				total = remaining
			}
			out = append(out, CreditSegment{
				Remaining:   round2(remaining),
				Total:       round2(total),
				ExpiresAt:   firstTimestamp(item, expiryFields),
				Source:      firstText(item, labelFields, SegmentLabelUnknown),
				PackageCode: asString(item["PackageCode"]),
			})
		}
	}
	return out
}

func mergeSegments(segments []CreditSegment) []CreditSegment {
	type key struct {
		code string
		exp  float64
		has  bool
	}
	merged := map[key]CreditSegment{}
	order := make([]key, 0)
	for _, segment := range segments {
		if segment.Remaining <= 0 {
			continue
		}
		k := key{code: firstNonEmpty(segment.PackageCode, segment.Source)}
		if segment.ExpiresAt != nil {
			k.exp, k.has = *segment.ExpiresAt, true
		}
		if existing, ok := merged[k]; ok {
			existing.Remaining += segment.Remaining
			existing.Total += segment.Total
			merged[k] = existing
			continue
		}
		merged[k] = segment
		order = append(order, k)
	}
	out := make([]CreditSegment, 0, len(order))
	for _, k := range order {
		segment := merged[k]
		segment.Remaining = round2(segment.Remaining)
		segment.Total = round2(segment.Total)
		out = append(out, segment)
	}
	sort.SliceStable(out, func(a, b int) bool { return expiryRank(out[a]) < expiryRank(out[b]) })
	return out
}

func soonestExpiry(segments []CreditSegment, now int64) *float64 {
	var best *float64
	for _, segment := range segments {
		if segment.ExpiresAt == nil || *segment.ExpiresAt <= float64(now) || segment.Remaining <= 0 {
			continue
		}
		if best == nil || *segment.ExpiresAt < *best {
			value := *segment.ExpiresAt
			best = &value
		}
	}
	return best
}

func firstNumber(item map[string]any, fields []string) (float64, bool) {
	for _, field := range fields {
		if n, ok := toFloat(item[field]); ok {
			return n, true
		}
	}
	return 0, false
}

func firstTimestamp(item map[string]any, fields []string) *float64 {
	for _, field := range fields {
		raw := item[field]
		if n, ok := toFloat(raw); ok && n > 0 {
			if n > 1e12 {
				n /= 1000
			}
			return &n
		}
		if s, ok := raw.(string); ok && len(strings.TrimSpace(s)) >= 19 {
			if t, err := time.ParseInLocation(billingTimeLayout, strings.TrimSpace(s)[:19], billingLocation()); err == nil {
				n := float64(t.Unix())
				return &n
			}
		}
	}
	return nil
}

// billingLocation is the zone billing timestamps are written in (Beijing).
func billingLocation() *time.Location {
	return time.FixedZone("CST", 8*3600)
}

func firstText(item map[string]any, fields []string, fallback string) string {
	for _, field := range fields {
		if s := strings.TrimSpace(asString(item[field])); s != "" {
			return s
		}
	}
	return fallback
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func round2(n float64) float64 {
	return math.Round(n*100) / 100
}
