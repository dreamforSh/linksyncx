package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/tidwall/gjson"
)

type perplexityThinkingContextKey struct{}
type perplexityThinkingContext struct{ enabled *bool }

// WithPerplexityThinkingContext keeps model-health filtering on the same variant
// that forwarding will select, without retaining the request body in context.
func WithPerplexityThinkingContext(ctx context.Context, body []byte) context.Context {
	opts := perplexity.DefaultRequestOptions()
	if !gjson.ValidBytes(body) {
		return ctx
	}
	if _, err := parsePerplexityThinkingIntent(gjson.ParseBytes(body), &opts); err != nil {
		return ctx
	}
	return context.WithValue(ctx, perplexityThinkingContextKey{}, perplexityThinkingContext{enabled: opts.Internal.ThinkingEnabled})
}

// parsePerplexityThinkingIntent runs on the unconverted ingress body. Effort
// enables a model variant, but its strength remains explicitly unimplemented.
func parsePerplexityThinkingIntent(root gjson.Result, opts *perplexity.RequestOptions) ([]string, error) {
	var enabled *bool
	var ignored []string
	set := func(value bool) error {
		if enabled != nil && *enabled != value {
			return perplexityOptionError("conflicting Perplexity thinking and reasoning options")
		}
		enabled = &value
		return nil
	}
	parseEffort := func(value gjson.Result, path string) error {
		if value.Raw == "" || value.Type == gjson.Null {
			return nil
		}
		if value.Type != gjson.String {
			return perplexityOptionError("%s must be a reasoning effort string", path)
		}
		switch value.String() {
		case "none":
			return set(false)
		case "minimal", "low", "medium", "high", "xhigh", "max":
			ignored = append(ignored, path+".strength")
			return set(true)
		case "auto":
			ignored = append(ignored, path+".strength")
			return nil
		default:
			return perplexityOptionError("unsupported %s", path)
		}
	}
	if thinking := root.Get("thinking"); thinking.Raw != "" && thinking.Type != gjson.Null {
		switch thinking.Type {
		case gjson.True, gjson.False:
			if err := set(thinking.Bool()); err != nil {
				return nil, err
			}
		default:
			if !thinking.IsObject() {
				return nil, perplexityOptionError("thinking must be a boolean or an object with type enabled, adaptive, or disabled")
			}
			switch thinking.Get("type").String() {
			case "enabled", "adaptive":
				if err := set(true); err != nil {
					return nil, err
				}
			case "disabled":
				if err := set(false); err != nil {
					return nil, err
				}
			default:
				return nil, perplexityOptionError("unsupported thinking.type")
			}
			for name := range thinking.Map() {
				switch name {
				case "type":
				case "display":
					ignored = append(ignored, "thinking.display")
				default:
					return nil, perplexityOptionError("unsupported Perplexity thinking option")
				}
			}
		}
	}
	if err := parseEffort(root.Get("reasoning_effort"), "reasoning_effort"); err != nil {
		return nil, err
	}
	if reasoning := root.Get("reasoning"); reasoning.Raw != "" && reasoning.Type != gjson.Null {
		if !reasoning.IsObject() {
			return nil, perplexityOptionError("reasoning must be an object")
		}
		if err := parseEffort(reasoning.Get("effort"), "reasoning.effort"); err != nil {
			return nil, err
		}
		for name := range reasoning.Map() {
			switch name {
			case "effort":
			case "summary", "generate_summary":
				ignored = append(ignored, "reasoning."+name)
			default:
				return nil, perplexityOptionError("unsupported Perplexity reasoning option")
			}
		}
	}
	if err := parseEffort(root.Get("output_config.effort"), "output_config.effort"); err != nil {
		return nil, err
	}
	opts.Internal.ThinkingEnabled = enabled
	return ignored, nil
}

func resolvePerplexityThinkingPreference(account *Account, preference string, opts perplexity.RequestOptions) (string, error) {
	if opts.Internal.ThinkingEnabled == nil {
		return preference, nil
	}
	var catalog []string
	if account != nil {
		snapshot := account.GetUpstreamModelMetadataSnapshot()
		if snapshot != nil && snapshot.Source == "perplexity" && snapshot.IdentityFingerprint != "" && snapshot.IdentityFingerprint == perplexityAccountFingerprint(account) {
			if syncedAt, err := time.Parse(time.RFC3339, snapshot.SyncedAt); err == nil && !syncedAt.After(time.Now()) && time.Since(syncedAt) < 24*time.Hour {
				for id := range snapshot.Models {
					catalog = append(catalog, strings.ToLower(id))
				}
			}
		}
	}
	selected, err := perplexity.ResolveThinkingPreference(preference, *opts.Internal.ThinkingEnabled, catalog)
	if err != nil {
		return "", perplexityOptionError("%s", err.Error())
	}
	return selected, nil
}
