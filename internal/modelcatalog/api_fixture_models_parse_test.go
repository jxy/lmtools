package modelcatalog

import (
	"encoding/json"
	"lmtools/internal/apifixtures"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIFixtureModelsParsing(t *testing.T) {
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}

	caseFilter := strings.TrimSpace(os.Getenv("LMTOOLS_API_FIXTURE_CASE"))
	providerFilter := strings.TrimSpace(os.Getenv("LMTOOLS_API_FIXTURE_PROVIDER"))

	for _, listedCase := range suite.Manifest.Cases {
		meta, err := apifixtures.LoadCaseMeta(suite.Root, listedCase.ID)
		if err != nil {
			t.Fatalf("LoadCaseMeta(%q) error = %v", listedCase.ID, err)
		}
		if !apifixtures.MatchesFilters(meta, caseFilter, providerFilter) {
			continue
		}
		if !apifixtures.StringSliceContains(meta.Kinds, "models") {
			continue
		}

		t.Run(meta.ID, func(t *testing.T) {
			responsePath := filepath.Join("captures", meta.Provider+".response.json")
			data, err := apifixtures.ReadCaseFile(suite.Root, meta.ID, responsePath)
			if err != nil {
				t.Fatalf("ReadCaseFile(%q) error = %v", responsePath, err)
			}

			items, err := Parse(meta.Provider, data)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v", meta.Provider, err)
			}

			wantBytes, err := apifixtures.ReadCaseFile(suite.Root, meta.ID, filepath.Join("expected", "parsed.json"))
			if err != nil {
				t.Fatalf("ReadCaseFile(expected/parsed.json) error = %v", err)
			}
			wantCanonical, err := apifixtures.CanonicalJSON(wantBytes)
			if err != nil {
				t.Fatalf("CanonicalJSON(want) error = %v", err)
			}

			actualBytes, err := json.Marshal(Project(items))
			if err != nil {
				t.Fatalf("json.Marshal(actual) error = %v", err)
			}
			actualCanonical, err := apifixtures.CanonicalJSON(actualBytes)
			if err != nil {
				t.Fatalf("CanonicalJSON(actual) error = %v", err)
			}

			if string(wantCanonical) != string(actualCanonical) {
				t.Fatalf("parsed models mismatch\nwant:\n%s\n\ngot:\n%s", wantCanonical, actualCanonical)
			}
		})
	}
}
