package runtime

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"connectrpc.com/connect/v2"
	pb "github.com/practice-library/machinery/gen/go/machinery/plugin/v1alpha1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const failureType = "machinery.plugin.v1alpha1.Failure"

// body is the error's Connect JSON body as sent, and the Failure its one detail decodes to (nil if none).
func body(t *testing.T, e *connect.Error) (map[string]any, *pb.Failure) {
	t.Helper()
	raw := errorBody(e)
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("the error body is not JSON: %v: %s", err, raw)
	}
	details, _ := b["details"].([]any)
	if len(details) == 0 {
		return b, nil
	}
	if len(details) != 1 {
		t.Fatalf("%d details, want one", len(details))
	}
	d := details[0].(map[string]any)
	if d["type"] != failureType {
		t.Fatalf("detail type %v, want %s", d["type"], failureType)
	}
	bin, err := base64.RawStdEncoding.DecodeString(d["value"].(string))
	if err != nil {
		t.Fatalf("detail value is not unpadded base64: %v", err)
	}
	f := new(pb.Failure)
	if err := proto.Unmarshal(bin, f); err != nil {
		t.Fatalf("detail does not decode as a Failure: %v", err)
	}
	return b, f
}

// Every code the runtime answers keeps its code, says its warnings joined by "; ", and carries the
// envelope as a Failure: ok false, data null, the same warnings and notRead.
func TestFailureCarriesItsEnvelope(t *testing.T) {
	warnings := []string{"first", "second"}
	notRead := []NotRead{{What: "input.x", Why: "not used"}}
	for _, code := range []connect.Code{connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeResourceExhausted,
		connect.CodeUnimplemented, connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeInternal} {
		e := failure(code, warnings, notRead, 1<<20)
		b, f := body(t, e)
		if e.Code() != code || b["code"] != code.String() {
			t.Errorf("%v: code %v / %v", code, e.Code(), b["code"])
		}
		if b["message"] != "first; second" {
			t.Errorf("%v: message %q", code, b["message"])
		}
		if f == nil {
			t.Fatalf("%v: no Failure detail", code)
		}
		if f.GetOk() || f.GetData().GetKind() == nil || f.GetData().GetNullValue() != structpb.NullValue_NULL_VALUE {
			t.Errorf("%v: ok %v data %v, want false and null", code, f.GetOk(), f.GetData())
		}
		if strings.Join(f.GetWarnings(), "|") != "first|second" || len(f.GetNotRead()) != 1 ||
			f.GetNotRead()[0].GetWhat() != "input.x" || f.GetNotRead()[0].GetWhy() != "not used" {
			t.Errorf("%v: Failure %v", code, f)
		}
	}
}

// A warning is cut to 500 characters, counted in code points, not bytes.
func TestFailureCutsAWarningTo500Characters(t *testing.T) {
	_, f := body(t, failure(connect.CodeInternal, []string{strings.Repeat("é", 501)}, nil, 1<<20))
	if n := utf8.RuneCountInString(f.GetWarnings()[0]); n != warningMax {
		t.Errorf("warning of %d characters, want %d", n, warningMax)
	}
}

// The body fits the limit by saying less, never by dropping the detail: notRead goes first, then every
// warning but the first, then the end of that warning. At every limit from 1024, the smallest outputBytes
// the contract allows, the body fits and the detail is there with a warning that starts the first one.
func TestFailureFitsByDroppingNotReadThenWarnings(t *testing.T) {
	long := []string{"w1 " + strings.Repeat("a", 497), "w2 " + strings.Repeat("b", 497), "w3 " + strings.Repeat("c", 497)}
	nr := []NotRead{{What: strings.Repeat("x", 300), Why: strings.Repeat("y", 300)}}
	full := len(errorBody(failure(connect.CodeInvalidArgument, long, nr, 1<<20)))
	noNotRead := len(errorBody(failure(connect.CodeInvalidArgument, long, nil, 1<<20)))
	for limit := 1024; limit <= full+1; limit++ {
		e := failure(connect.CodeInvalidArgument, long, nr, limit)
		raw := errorBody(e)
		if len(raw) > limit {
			t.Fatalf("limit %d: body of %d bytes", limit, len(raw))
		}
		b, f := body(t, e)
		if f == nil || len(f.GetWarnings()) == 0 || !strings.HasPrefix(long[0], f.GetWarnings()[0]) || f.GetWarnings()[0] == "" {
			t.Fatalf("limit %d: detail %v, want the first warning or a prefix of it", limit, f)
		}
		if b["message"] != strings.Join(f.GetWarnings(), "; ") {
			t.Fatalf("limit %d: message %q is not the detail's warnings joined", limit, b["message"])
		}
		switch {
		case limit >= full:
			if len(f.GetNotRead()) != 1 || len(f.GetWarnings()) != 3 {
				t.Fatalf("limit %d: shrank though the whole error fits", limit)
			}
		case limit >= noNotRead:
			if len(f.GetNotRead()) != 0 || len(f.GetWarnings()) != 3 {
				t.Fatalf("limit %d: want notRead dropped and every warning kept, got %v", limit, f)
			}
		default:
			if len(f.GetNotRead()) != 0 || len(f.GetWarnings()) != 1 {
				t.Fatalf("limit %d: want the first warning alone, got %d warnings", limit, len(f.GetWarnings()))
			}
		}
	}
}

// Below 1024 the contract allows no outputBytes; only there, where not one character fits, is the detail
// dropped, and with no limit at all nothing is cut.
func TestFailureBelowAnyFittingLimit(t *testing.T) {
	e := failure(connect.CodeInvalidArgument, []string{"x"}, nil, 10)
	if _, f := body(t, e); f != nil || e.Code() != connect.CodeInvalidArgument {
		t.Errorf("limit 10: detail %v, code %v; want no detail and the code kept", f, e.Code())
	}
	_, f := body(t, failure(connect.CodeInvalidArgument, []string{strings.Repeat("z", 500)}, []NotRead{{What: "a", Why: "b"}}, 0))
	if f == nil || len(f.GetWarnings()[0]) != 500 || len(f.GetNotRead()) != 1 {
		t.Errorf("no limit: %v, want everything kept", f)
	}
}

// warnings and notRead outside envelope.schema.json's bounds are named, so the runtime answers internal.
func TestCheckStringsBounds(t *testing.T) {
	ok := func(n int) string { return strings.Repeat("é", n) }
	for _, c := range []struct {
		warnings []string
		notRead  []NotRead
		bad      bool
	}{
		{[]string{ok(1)}, nil, false}, {[]string{ok(500)}, nil, false}, {[]string{""}, nil, true}, {[]string{ok(501)}, nil, true},
		{nil, []NotRead{{ok(1), ok(300)}}, false}, {nil, []NotRead{{"", "x"}}, true}, {nil, []NotRead{{"x", ok(301)}}, true},
		{nil, []NotRead{{ok(301), "x"}}, true},
	} {
		if got := checkStrings(c.warnings, c.notRead) != ""; got != c.bad {
			t.Errorf("%d warnings, %v: refused %v, want %v", len(c.warnings), c.notRead, got, c.bad)
		}
	}
}

// A warning that is not UTF-8 cannot go in a proto string; the error still has a detail, saying so.
func TestFailureNotUTF8(t *testing.T) {
	_, f := body(t, failure(connect.CodeInternal, []string{"bad \xff"}, nil, 1<<20))
	if f == nil || f.GetWarnings()[0] != "the error's warnings are not UTF-8" {
		t.Errorf("detail %v", f)
	}
}
