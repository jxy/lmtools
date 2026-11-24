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
