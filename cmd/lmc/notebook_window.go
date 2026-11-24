package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"lmtools/internal/config"
	"lmtools/internal/contextcfg"
	"lmtools/internal/core"
	"lmtools/internal/modelcatalog"
	"lmtools/internal/session"
	"time"
)

// resolveWindow sets the window of a run with context settings, once, for
// the selected model: a run whose policy compacts, a notebook run under the
// default policy, and a run with a cap. The operator's cap and the model's
// capacity bound the window: the smaller when both are known, the one known
// otherwise. The capacity comes from the newest reply in the lineage made
// with the same model and endpoint, else from the provider's model catalog;
// an offline preview reads only the lineage. Without a window, a notebook's
// default policy is full replay, and a compacting policy is refused.
func resolveWindow(ctx context.Context, cfg *config.Config, opts *core.RequestOptions, notice core.Notifier, logDir string, lineage []session.Message) error {
	b := opts.Context
	if !b.Compacts() && !b.NotebookDefault && b.WindowCap == 0 {
		return nil
	}
	model := core.EffectiveChatModel(*opts)
	endpoint := windowEndpoint(cfg, *opts)
	capacity := recordedCapacity(lineage, model, endpoint)
	if capacity == 0 && !cfg.PrintCurl {
		capacity = catalogCapacity(ctx, *cfg, model, logDir)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	window := b.WindowCap
	if capacity > 0 && (window == 0 || capacity < window) {
		window = capacity
	}
	if cfg.PrintCurl && window == 0 && (b.Compacts() || b.NotebookDefault) {
		return fmt.Errorf("cannot preview the context window for %s offline; specify -context-window for this model or use -context-policy full for full replay", model)
	}
	b.Window, b.Capacity = window, contextcfg.Capacity{}
	if capacity > 0 {
		b.Capacity = contextcfg.Capacity{Tokens: capacity, Model: model, Endpoint: endpoint}
	}
	if b.NotebookDefault {
		b.Policy = contextcfg.Handoff
		if window == 0 && b.Trigger == 0 {
			b.Policy = contextcfg.Full
			notice.Warnf("No context window available for %s. Using full replay with the notebook. Set -context-window to enable handoff.", model)
		}
	}
	if err := b.Validate(); err != nil {
		return fmt.Errorf("context settings for %s: %w", model, err)
	}
	cfg.Context, opts.Context = b, b
	return nil
}

// windowEndpoint identifies where a model's capacity was found: the
// provider, its endpoints and environment.
func windowEndpoint(cfg *config.Config, opts core.RequestOptions) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(opts.Provider+"\x00"+opts.ProviderURL+"\x00"+cfg.ModelsURL+"\x00"+opts.Env)))
}

// recordedCapacity returns the capacity the newest reply in lineage records
// for model at endpoint, or zero.
func recordedCapacity(lineage []session.Message, model, endpoint string) int {
	for i := len(lineage) - 1; i >= 0; i-- {
		if r := lineage[i].Receipt; r != nil && r.Capacity != nil && r.Capacity.Model == model && r.Capacity.Endpoint == endpoint {
			return r.Capacity.Tokens
		}
	}
	return 0
}

// catalogCapacity returns the context size the provider's model catalog
// gives for model, or zero when the catalog cannot be read or has none.
func catalogCapacity(ctx context.Context, cfg config.Config, model, logDir string) int {
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	items, err := fetchModelCatalog(lookup, cfg, logDir)
	if err != nil {
		return 0
	}
	for _, item := range items {
		if item.ID == model {
			if n := modelcatalog.Describe(item).ContextTokens; n >= 2048 && n <= 1000000000 {
				return int(n)
			}
			return 0
		}
	}
	return 0
}
