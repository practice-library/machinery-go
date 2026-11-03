// Command minimal is a minimal plugin on the runtime, for conformance: it speaks the fact-refresh job's
// schemas, so machinery's Connect runner can check its answers, but it refreshes nothing. Each call shows
// one outcome:
//
//	Describe  its name, version, job and verbs; the runtime adds protocol and contracts.
//	Plan      no work: a plan with no units, decided nothing (maxParallel 1 shows integers leave as 1,
//	          not 1.0); failed_precondition when the graph has nodes; it overruns its caps, ignoring ctx,
//	          when timeSeconds is 1.
//	RunUnit   unavailable: it never fetches, so it always asks to be tried later.
//	Merge     an answer larger than outputBytes, which the runtime refuses (resource_exhausted).
//	Run       not a method here: the runtime answers unimplemented.
//
//	minimal [-port 0]   # prints its base URL first
//	minimal -stdio      # one call on stdin and stdout
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/practice-library/machinery-go/runtime"
)

const (
	schemas = "https://github.com/practice-library/machinery/contract/v1alpha1/fact-refresh.schema.json#/$defs/"
	report  = "https://github.com/practice-library/.github/spec/refresh-report/schema.json"
)

type minimal struct{}

func (minimal) Describe(context.Context, runtime.Caps) (runtime.Answer, error) {
	verb := func(name string, shardable bool, input, output string) map[string]any {
		return map[string]any{"name": name, "shardable": shardable, "input": input, "output": output}
	}
	return runtime.Answer{Data: map[string]any{
		"name": "minimal", "version": "0.1.0", "job": "fact-refresh",
		"verbs": []any{
			verb("plan", false, schemas+"planInput", schemas+"plan"),
			verb("run-unit", true, schemas+"unit", schemas+"partial"),
			verb("merge", false, schemas+"mergeInput", report),
		},
	}}, nil
}

func (minimal) Plan(_ context.Context, input map[string]any, caps runtime.Caps) (runtime.Answer, error) {
	if caps.TimeSeconds == 1 {
		time.Sleep(5 * time.Second) // deliberately deaf to ctx: the runtime answers on time anyway
	}
	if nodes, _ := input["nodes"].([]any); len(nodes) > 0 {
		return runtime.Answer{}, runtime.Refusal{Code: connect.CodeFailedPrecondition,
			Warnings: []string{fmt.Sprintf("this example plans no node, and the graph has %d", len(nodes))}}
	}
	return runtime.Answer{Data: map[string]any{
		"graph": input["graph"], "commit": input["commit"], "run": input["run"], "startedAt": input["now"],
		"maxParallel": 1, "units": []any{}, "decided": []any{},
	}}, nil
}

func (minimal) RunUnit(context.Context, map[string]any, runtime.Network, runtime.Caps) (runtime.Answer, error) {
	return runtime.Answer{}, runtime.Refusal{Code: connect.CodeUnavailable,
		Warnings: []string{"this example never fetches: try the unit again later"}}
}

func (minimal) Merge(_ context.Context, _ map[string]any, caps runtime.Caps) (runtime.Answer, error) {
	return runtime.Answer{Data: map[string]any{"padding": strings.Repeat("x", caps.OutputBytes)}}, nil
}

func main() { runtime.Main(minimal{}) }
