package contextcfg

func (s Settings) PhaseHints() bool  { return s.Early }
func (s Settings) CutThreshold() int { return s.Threshold() }
func (Settings) ArchiveOnly() bool   { return false }
