package core

import (
	"encoding/json"
	"lmtools/internal/apifixtures"
	"os"
	"strings"
	"testing"
)

func TestAPIFixtureResponseParsing(t *testing.T) {
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
		if !apifixtures.StringSliceContains(meta.Kinds, "response") {
			continue
		}

		t.Run(meta.ID, func(t *testing.T) {
			responsePath := "captures/" + meta.Provider + ".response.json"
			data, err := apifixtures.ReadCaseFile(suite.Root, meta.ID, responsePath)
			if err != nil {
				t.Fatalf("ReadCaseFile(%q) error = %v", responsePath, err)
			}
			projected, err := ParseResponseProjection(meta.Provider, data)
			if err != nil {
				t.Fatalf("parse response error = %v", err)
			}

			wantBytes, err := apifixtures.ReadCaseFile(suite.Root, meta.ID, "expected/parsed.json")
			if err != nil {
				t.Fatalf("ReadCaseFile(expected/parsed.json) error = %v", err)
			}
			wantCanonical, err := apifixtures.CanonicalJSON(wantBytes)
			if err != nil {
				t.Fatalf("CanonicalJSON(want) error = %v", err)
			}

			actualBytes, err := json.Marshal(projected)
			if err != nil {
				t.Fatalf("json.Marshal(actual) error = %v", err)
			}
			actualCanonical, err := apifixtures.CanonicalJSON(actualBytes)
			if err != nil {
				t.Fatalf("CanonicalJSON(actual) error = %v", err)
			}

			if string(wantCanonical) != string(actualCanonical) {
				t.Fatalf("parsed response mismatch\nwant:\n%s\n\ngot:\n%s", wantCanonical, actualCanonical)
			}
		})
	}
}
