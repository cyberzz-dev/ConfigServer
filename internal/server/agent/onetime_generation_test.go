// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/alibaba/ilogtail/config_server/internal/model"
)

func decodeDetail(t *testing.T, b []byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("detail is not a JSON object: %v", err)
	}
	return m
}

// A same-named command recreated with identical user content but a new CreatedAt
// must produce different delivered bytes (and thus a different content hash), so the
// agent re-delivers and re-runs it instead of resuming the old checkpoint.
func TestOnetimeDetailWithGeneration_SameContentDifferentGenerationDiffers(t *testing.T) {
	content := []byte(`{"global":{"ExcutionTimeout":60},"inputs":[{"Type":"input_static_file"}]}`)

	first := &model.OnetimeCommand{Name: "cmd", Detail: content, CreatedAt: time.Unix(0, 1000)}
	second := &model.OnetimeCommand{Name: "cmd", Detail: content, CreatedAt: time.Unix(0, 2000)}

	d1 := onetimeDetailWithGeneration(first)
	d2 := onetimeDetailWithGeneration(second)

	if onetimeContentHash(d1) == onetimeContentHash(d2) {
		t.Fatalf("expected different content hashes for different generations")
	}

	g1 := decodeDetail(t, d1)["global"].(map[string]interface{})
	if got := g1[onetimeGenerationKey]; got != float64(1000) {
		t.Fatalf("expected generation 1000, got %v", got)
	}
	// Existing global fields must be preserved.
	if got := g1["ExcutionTimeout"]; got != float64(60) {
		t.Fatalf("expected ExcutionTimeout 60 preserved, got %v", got)
	}
}

// The same command delivered twice (same generation) must be byte-stable, so the
// agent's content-hash de-duplication keeps treating it as already-applied.
func TestOnetimeDetailWithGeneration_SameGenerationStable(t *testing.T) {
	content := []byte(`{"global":{"ExcutionTimeout":60},"inputs":[{"Type":"input_static_file"}]}`)
	oc := &model.OnetimeCommand{Name: "cmd", Detail: content, CreatedAt: time.Unix(0, 1234)}

	if onetimeContentHash(onetimeDetailWithGeneration(oc)) != onetimeContentHash(onetimeDetailWithGeneration(oc)) {
		t.Fatalf("expected stable hash for the same generation")
	}
}

// A detail with no "global" object must get one created with the generation injected.
func TestOnetimeDetailWithGeneration_CreatesGlobalWhenMissing(t *testing.T) {
	oc := &model.OnetimeCommand{
		Name:      "cmd",
		Detail:    []byte(`{"inputs":[{"Type":"input_static_file"}]}`),
		CreatedAt: time.Unix(0, 777),
	}
	global, ok := decodeDetail(t, onetimeDetailWithGeneration(oc))["global"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected a global object to be created")
	}
	if got := global[onetimeGenerationKey]; got != float64(777) {
		t.Fatalf("expected generation 777, got %v", got)
	}
}

// Large int64 parameters in the detail must survive verbatim (UseNumber avoids
// float64 precision loss).
func TestOnetimeDetailWithGeneration_PreservesLargeInt64(t *testing.T) {
	const big = "9007199254740993" // 2^53 + 1, not representable as float64
	oc := &model.OnetimeCommand{
		Name:      "cmd",
		Detail:    []byte(`{"global":{"ExcutionTimeout":60},"inputs":[{"Big":` + big + `}]}`),
		CreatedAt: time.Unix(0, 1),
	}
	out := onetimeDetailWithGeneration(oc)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	var inputs []map[string]json.RawMessage
	if err := json.Unmarshal(raw["inputs"], &inputs); err != nil {
		t.Fatalf("unmarshal inputs failed: %v", err)
	}
	if got := string(inputs[0]["Big"]); got != big {
		t.Fatalf("expected large int %s preserved, got %s", big, got)
	}
}

// Non-JSON or zero-generation details must be returned unchanged (backward compatible).
func TestOnetimeDetailWithGeneration_FallbackUnchanged(t *testing.T) {
	// Zero CreatedAt -> generation <= 0 -> returned as-is.
	zeroGen := &model.OnetimeCommand{Name: "cmd", Detail: []byte(`{"global":{}}`)}
	if string(onetimeDetailWithGeneration(zeroGen)) != `{"global":{}}` {
		t.Fatalf("expected detail unchanged for zero generation")
	}
	// Non-object JSON -> returned as-is.
	notObj := &model.OnetimeCommand{Name: "cmd", Detail: []byte(`[1,2,3]`), CreatedAt: time.Unix(0, 5)}
	if string(onetimeDetailWithGeneration(notObj)) != `[1,2,3]` {
		t.Fatalf("expected non-object detail unchanged")
	}
}
