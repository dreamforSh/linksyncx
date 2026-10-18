package codebuddy

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

const (
	// RequestUsageMaxDays is an upstream hard limit: a range wider than 31 days
	// silently returns an empty total instead of an error, so the window is
	// clamped rather than trusted to the caller.
	RequestUsagePath     = "/billing/meter/get-user-request-usage" // no /v2 prefix
	RequestUsageMaxDays  = 30
	requestUsagePageSize = 200
	// requestUsageMaxPages bounds the paging loop so a mis-reported total cannot
	// spin forever.
	requestUsageMaxPages = 30
)

// RequestUsageDay is one calendar day of consumption as CodeBuddy billed it.
type RequestUsageDay struct {
	Day      string             `json:"day"`
	Credits  float64            `json:"credits"`
	Requests int64              `json:"requests"`
	ByModel  map[string]float64 `json:"by_model,omitempty"`
}

// RequestUsage is the authoritative consumption report from CodeBuddy's billing
// API. Unlike the gateway's own tally it covers every client that used the
// account, including the official IDE, and reflects the real charged amount
// rather than a multiplier lookup.
type RequestUsage struct {
	Days         []RequestUsageDay `json:"days"`
	TotalCredits float64           `json:"total_credits"`
	Requests     int64             `json:"requests"`
	RangeDays    int               `json:"range_days"`
	FetchedAt    int64             `json:"fetched_at"`
}

// Today returns the entry for the caller's local calendar day, if present.
func (u *RequestUsage) Today(now time.Time) (RequestUsageDay, bool) {
	if u == nil {
		return RequestUsageDay{}, false
	}
	day := now.In(billingLocation()).Format(usageDayLayout)
	for _, entry := range u.Days {
		if entry.Day == day {
			return entry, true
		}
	}
	return RequestUsageDay{}, false
}

// FetchRequestUsage pulls per-request credit consumption for the trailing window
// and folds it into day × model buckets.
func FetchRequestUsage(ctx context.Context, client HTTPDoer, creds Credentials, days int) (*RequestUsage, error) {
	if days <= 0 || days > RequestUsageMaxDays {
		days = RequestUsageMaxDays
	}

	host := BillingHost(creds.Profile)
	headers := WebHeaders(host, IdentityOf(creds))
	now := time.Now().In(billingLocation())
	window := map[string]any{
		"startTime": now.AddDate(0, 0, -days).Format(billingTimeLayout),
		"endTime":   now.Format(billingTimeLayout),
	}

	byDay := map[string]*RequestUsageDay{}
	total := 0.0
	var requests int64
	for page := 1; page <= requestUsageMaxPages; page++ {
		body := map[string]any{
			"startTime": window["startTime"],
			"endTime":   window["endTime"],
			"pageNum":   page,
			"pageSize":  requestUsagePageSize,
		}
		payload, err := postBillingJSON(ctx, client, host+RequestUsagePath, headers, body)
		if err != nil {
			return nil, err
		}
		rows, reported := parseRequestUsagePage(payload)
		for _, row := range rows {
			day := byDay[row.day]
			if day == nil {
				day = &RequestUsageDay{Day: row.day, ByModel: map[string]float64{}}
				byDay[row.day] = day
			}
			day.Credits = round2(day.Credits + row.credit)
			day.Requests++
			day.ByModel[row.model] = round2(day.ByModel[row.model] + row.credit)
			total += row.credit
			requests++
		}
		if len(rows) == 0 || (reported > 0 && requests >= reported) {
			break
		}
	}

	out := &RequestUsage{
		Days:         make([]RequestUsageDay, 0, len(byDay)),
		TotalCredits: round2(total),
		Requests:     requests,
		RangeDays:    days,
		FetchedAt:    time.Now().Unix(),
	}
	for _, day := range byDay {
		out.Days = append(out.Days, *day)
	}
	// Newest first: the admin UI leads with today.
	sort.Slice(out.Days, func(a, b int) bool { return out.Days[a].Day > out.Days[b].Day })
	return out, nil
}

type requestUsageRow struct {
	day    string
	model  string
	credit float64
}

// parseRequestUsagePage extracts rows plus the upstream's reported row count,
// which the paging loop uses as its stop condition.
func parseRequestUsagePage(payload []byte) ([]requestUsageRow, int64) {
	var envelope struct {
		Data struct {
			Data  []map[string]any `json:"data"`
			Total json.Number      `json:"total"`
		} `json:"data"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return nil, 0
	}
	rows := make([]requestUsageRow, 0, len(envelope.Data.Data))
	for _, raw := range envelope.Data.Data {
		day := strings.TrimSpace(asString(raw["requestTime"]))
		if len(day) < 10 {
			continue
		}
		credit, _ := toFloat(raw["credit"])
		model := strings.TrimSpace(asString(raw["model"]))
		if model == "" {
			model = "unknown"
		}
		rows = append(rows, requestUsageRow{day: day[:10], model: model, credit: credit})
	}
	total, err := envelope.Data.Total.Int64()
	if err != nil {
		total = 0
	}
	return rows, total
}
