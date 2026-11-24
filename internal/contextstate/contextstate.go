// Package contextstate holds what the context engine keeps beside a
// conversation: a ref describing each message of a request's history, the
// checkpoints that replace a history's beginning with a handoff, the receipt
// a reply records of the window it was produced with, and the standing pins
// a user marks. Session stores persist these values and the engine reads
// them; neither needs the other.
package contextstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"lmtools/internal/contextcfg"
	"lmtools/internal/core"
	"time"
)

// Ref describes one message of a request's history.
type Ref struct {
	// Source names the message in history refs and citations, such as the
	// ref a paged tool result points to.
	Source     string
	At         time.Time
	Role       core.Role
	Receipt    *Receipt
	Pin        *PinChange
	Incomplete bool
}

// Receipt is what a reply records of the window it was produced with.
type Receipt struct {
	// Count is the number of messages the window's checkpoint covers, and
	// Prefix and Tag find that checkpoint.
	Count     int    `json:"count"`
	Prefix    string `json:"prefix,omitempty"`
	Tag       string `json:"tag,omitempty"`
	PinDigest string `json:"pin_digest,omitempty"`
	Deferred  string `json:"deferred_checkpoint,omitempty"`
	// CatalogSent and CatalogOmitted record whether a window starting at
	// the beginning of the branch carried the notebook catalog or left it
	// out. Later requests in that window make the same choice, which keeps
	// their prefix stable, while the notebook and view are the ones the
	// choice was made for.
	CatalogSent    bool   `json:"catalog_sent,omitempty"`
	CatalogOmitted bool   `json:"catalog_omitted,omitempty"`
	Notebook       string `json:"catalog_notebook,omitempty"`
	View           string `json:"catalog_view,omitempty"`
	// Capacity is the model's context capacity the request knew, with the
	// model and endpoint it was found for. A later run with the same model
	// and endpoint takes it from the newest reply that records it.
	Capacity *contextcfg.Capacity `json:"capacity,omitempty"`
}

// Checkpoint replaces the first Count messages of a history with a handoff.
type Checkpoint struct {
	Tag string `json:"-"`
	// Window and Threshold are the budget the handoff was written for. Only
	// a different budget makes a cut rebuild from the original records.
	Window    int    `json:"window,omitempty"`
	Threshold int    `json:"threshold,omitempty"`
	Prefix    string `json:"prefix"`
	Count     int    `json:"count"`
	Handoff   string `json:"handoff"`
	Tail      string `json:"tail,omitempty"`
	Catalog   string `json:"catalog,omitempty"`
	// Notebook and View name the notebook and view the catalog came from.
	// A request under another one sends that one's catalog instead.
	Notebook    string        `json:"notebook,omitempty"`
	View        string        `json:"notebook_view,omitempty"`
	Continue    bool          `json:"continue,omitempty"`
	Pins        []StandingPin `json:"pins"`
	PinDigest   string        `json:"pin_digest,omitempty"`
	Fallback    bool          `json:"archive_fallback,omitempty"`
	Interrupted string        `json:"interrupted_work,omitempty"`
	// PagedResult names the original result message when its text cannot
	// fit in one window. Block indices locate each result within it.
	PagedResult string `json:"paged_result,omitempty"`
}

// PrefixKeys identifies each prefix of a history by the sources and pin
// events of its messages, in order. Two histories that share a key share
// every message up to it.
func PrefixKeys(refs []Ref) []string {
	keys := make([]string, len(refs))
	h := sha256.New()
	for i, r := range refs {
		fmt.Fprintln(h, r.Source)
		if r.Pin != nil {
			_ = json.NewEncoder(h).Encode(r.Pin)
		}
		keys[i] = hex.EncodeToString(h.Sum(nil))
	}
	return keys
}
