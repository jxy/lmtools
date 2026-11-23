package config

import (
	"flag"
	"fmt"
	"lmtools/internal/contextcfg"
)

func registerContextChoices(fs *flag.FlagSet, s *contextcfg.Settings) {
	fs.StringVar(&s.Policy, "context-policy", "", "session context: full or handoff")
	fs.StringVar(&s.View, "notebook-view", "", "notebook view at a window boundary: catalog (default) or tools alone")
	fs.Func("context-format", "compatibility spelling for the prose checkpoint writer", func(value string) error {
		if value != "prose" && value != "" {
			return fmt.Errorf("format %q requires an executable from lmc-context-bench", value)
		}
		s.Format = value
		return nil
	})
}

func selectContextChoices(s *contextcfg.Settings, saved contextcfg.Settings, found bool, explicit map[string]bool) error {
	if explicit["notebook-view"] && s.View == "full" && (!found || saved.View != s.View) {
		return fmt.Errorf("the full notebook view is available in lmc-context-bench; choose catalog or tools")
	}
	if explicit["context-policy"] && !explicit["context-format"] {
		s.Format = ""
	}
	if s.ExperimentalCompaction() {
		if found && !explicit["context-policy"] {
			return fmt.Errorf("saved session uses experimental context settings (policy %q, format %q); resume with -context-policy handoff or full, or use lmc-context-bench for the original settings", s.Policy, s.Format)
		}
		return fmt.Errorf("set -context-policy to full or handoff (experimental policies are available in lmc-context-bench)")
	}
	return nil
}
