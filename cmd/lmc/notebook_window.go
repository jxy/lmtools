package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"lmtools/internal/config"
	"lmtools/internal/contextcfg"
	"lmtools/internal/core"
	"lmtools/internal/modelcatalog"
	"time"
)

// Resolve capacity once for the selected model. A saved operator cap applies
// across model switches, while a limit obtained from a catalog belongs to its model.
func resolveNotebookWindow(ctx context.Context, cfg *config.Config, opts *core.RequestOptions, notice core.Notifier, logDir string) error {
	b := opts.Context
	if b.Notebook == "" || (!b.Compacts() && !b.NotebookDefault) {
		return nil
	}
	model := core.EffectiveChatModel(*opts)
	provider := fmt.Sprintf("%x", sha256.Sum256([]byte(opts.Provider+"\x00"+opts.ProviderURL+"\x00"+cfg.ModelsURL+"\x00"+opts.Env)))
	if b.WindowModel == model && b.WindowProvider == provider && (b.Window > 0 || cfg.PrintCurl && !b.Compacts()) {
		return b.Validate()
	}
	if cfg.PrintCurl && (b.WindowModel != "" || (b.WindowCap == 0 && b.Window == 0)) {
		return fmt.Errorf("cannot preview the context window for %s offline; specify -context-window for this model or use -context-policy full for full replay", model)
	}
	limit := b.WindowCap
	if limit == 0 && b.WindowModel == "" {
		limit = b.Window
	}
	if !cfg.PrintCurl {
		lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
		items, err := fetchModelCatalog(lookup, *cfg, logDir)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			for _, item := range items {
				if item.ID == model {
					n := modelcatalog.Describe(item).ContextTokens
					if n >= 2048 && n <= 1000000000 && (limit == 0 || int(n) < limit) {
						limit = int(n)
					}
					break
				}
			}
		}
	}
	b.Window, b.WindowModel = limit, model
	b.WindowProvider = provider
	if b.NotebookDefault {
		b.Policy = contextcfg.Handoff
	}
	if limit == 0 && b.NotebookDefault && b.Trigger == 0 {
		b.Policy = contextcfg.Full
		notice.Warnf("No context window available for %s. Using full replay with the notebook. Set -context-window to enable handoff.", model)
	}
	if err := b.Validate(); err != nil {
		return fmt.Errorf("context settings for %s: %w", model, err)
	}
	cfg.Context, opts.Context = b, b
	return nil
}
