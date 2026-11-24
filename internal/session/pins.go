package session

import (
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
)

// PinEntry is the pin event that makes change: a user message whose second
// block holds the pinned text, which an ordinary history ref names.
func PinEntry(change contextstate.PinChange, text string, active []contextstate.StandingPin) (Entry, error) {
	change, blocks, err := contextstate.PinBlocks(change, text, active)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Role: core.RoleUser, Pin: &change, Content: Content{Blocks: blocks}}, nil
}
