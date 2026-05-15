package apifixtures

import "testing"

func TestSubstituteStatefulValueRecursesThroughJSONShapes(t *testing.T) {
	vars := map[string]string{"response_id": "resp_123"}
	got := SubstituteStatefulValue(map[string]interface{}{
		"id": "${response_id}",
		"items": []interface{}{
			"before-${response_id}",
			map[string]interface{}{"nested": "${missing}"},
		},
	}, vars)

	want := map[string]interface{}{
		"id": "resp_123",
		"items": []interface{}{
			"before-resp_123",
			map[string]interface{}{"nested": "${missing}"},
		},
	}
	if !StatefulValuesEqual(got, want) {
		t.Fatalf("SubstituteStatefulValue() = %#v, want %#v", got, want)
	}
}

func TestLookupStatefulJSONPathSupportsIndexesAndLength(t *testing.T) {
	decoded := map[string]interface{}{
		"data": []interface{}{
			map[string]interface{}{"id": "item_0"},
			map[string]interface{}{"id": "item_1"},
		},
	}

	if got, ok := LookupStatefulJSONPath(decoded, "data.1.id"); !ok || got != "item_1" {
		t.Fatalf("LookupStatefulJSONPath(data.1.id) = %#v, %v; want item_1, true", got, ok)
	}
	if got, ok := LookupStatefulJSONPath(decoded, "data.length"); !ok || got != float64(2) {
		t.Fatalf("LookupStatefulJSONPath(data.length) = %#v, %v; want 2, true", got, ok)
	}
	if _, ok := LookupStatefulJSONPath(decoded, "data.2.id"); ok {
		t.Fatal("LookupStatefulJSONPath(data.2.id) ok = true, want false")
	}
}

func TestStatefulFieldsMatchSubstitutesAndComparesNumbers(t *testing.T) {
	decoded := map[string]interface{}{
		"id":    "resp_123",
		"count": float64(2),
	}
	fields := map[string]interface{}{
		"id":    "${response_id}",
		"count": 2,
	}
	if !StatefulFieldsMatch(decoded, fields, map[string]string{"response_id": "resp_123"}) {
		t.Fatal("StatefulFieldsMatch() = false, want true")
	}
}
