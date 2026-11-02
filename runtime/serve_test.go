package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	pb "github.com/practice-library/machinery/gen/go/machinery/plugin/v1alpha1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const svc = "/machinery.plugin.v1alpha1.PluginService/"

var caps = map[string]any{"timeSeconds": 60, "outputBytes": 1048576}

// testPlugin answers Describe, Plan and RunUnit as the test sets it, and records what it was called with.
type testPlugin struct {
	mu       sync.Mutex
	calls    []any
	answer   func(ctx context.Context, input map[string]any, caps Caps) (Answer, error)
	describe func() (Answer, error)
}

func (p *testPlugin) record(v ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, v)
}

func (p *testPlugin) Describe(_ context.Context, c Caps) (Answer, error) {
	p.record("Describe", c)
	if p.describe != nil {
		return p.describe()
	}
	return Answer{Data: map[string]any{"name": "t", "version": "1.0.0", "job": "j", "verbs": []any{}, "protocol": "wrong"}}, nil
}

func (p *testPlugin) Plan(ctx context.Context, input map[string]any, c Caps) (Answer, error) {
	p.record("Plan", input, c)
	return p.answer(ctx, input, c)
}

func (p *testPlugin) RunUnit(_ context.Context, input map[string]any, net Network, c Caps) (Answer, error) {
	p.record("RunUnit", input, net, c)
	return Answer{Data: map[string]any{}}, nil
}

func answers(a Answer, err error) func(context.Context, map[string]any, Caps) (Answer, error) {
	return func(context.Context, map[string]any, Caps) (Answer, error) { return a, err }
}

func serve(t *testing.T, p *testPlugin) string {
	t.Helper()
	srv := httptest.NewServer(Handler(p))
	t.Cleanup(srv.Close)
	return srv.URL
}

type reply struct {
	status int
	ctype  string
	body   []byte
}

func post(t *testing.T, url, call string, body any, headers ...string) reply {
	t.Helper()
	data, ok := body.([]byte)
	if !ok {
		data, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(http.MethodPost, url+svc+call, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableCompression: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, resp.Header.Get("Content-Type"), raw}
}

func plan(t *testing.T, p *testPlugin, url string, a Answer, err error, over ...any) reply {
	t.Helper()
	p.answer = answers(a, err)
	c := map[string]any{"timeSeconds": 60, "outputBytes": 1048576}
	for i := 0; i+1 < len(over); i += 2 {
		c[over[i].(string)] = over[i+1]
	}
	return post(t, url, "Plan", map[string]any{"caps": c, "input": map[string]any{"k": 1}})
}

type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details []struct {
		Type  string `json:"type"`
		Value string `json:"value"`
		Debug any    `json:"debug"`
	} `json:"details"`
}

// failureOf checks the HTTP status first, so a runtime that answers a success or another error fails on
// the status and not on a missing detail (machinery-python#13 to #16); then the error and its Failure.
func failureOf(t *testing.T, r reply, status int, code string) (wireError, *pb.Failure) {
	t.Helper()
	if r.status != status {
		t.Fatalf("HTTP %d, want %d: %s", r.status, status, r.body)
	}
	var e wireError
	if err := json.Unmarshal(r.body, &e); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
	if e.Code != code {
		t.Fatalf("code %s, want %s: %s", e.Code, code, r.body)
	}
	if len(e.Details) != 1 {
		t.Fatalf("the error has %d details, want one Failure: %s", len(e.Details), r.body)
	}
	if e.Details[0].Type != "machinery.plugin.v1alpha1.Failure" || e.Details[0].Debug != nil {
		t.Fatalf("detail %+v", e.Details[0])
	}
	raw, err := base64.RawStdEncoding.DecodeString(e.Details[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	f := new(pb.Failure)
	if err := proto.Unmarshal(raw, f); err != nil {
		t.Fatal(err)
	}
	if f.GetOk() || f.GetData().GetKind() == nil || f.GetData().AsInterface() != nil || len(f.GetWarnings()) == 0 {
		t.Fatalf("not a Failure envelope: %v", f)
	}
	if e.Message != strings.Join(f.GetWarnings(), "; ") {
		t.Fatalf("message %q, warnings %q", e.Message, f.GetWarnings())
	}
	return e, f
}

func TestDescribeGetsTheRuntimesPackageWhateverThePluginSays(t *testing.T) {
	url := serve(t, &testPlugin{})
	r := post(t, url, "Describe", map[string]any{"caps": caps})
	if r.status != 200 || r.ctype != "application/json" {
		t.Fatal(r.status, r.ctype)
	}
	var env struct{ Data map[string]any }
	_ = json.Unmarshal(r.body, &env)
	if env.Data["protocol"] != Package || !reflect.DeepEqual(env.Data["contracts"], []any{Package}) {
		t.Error(string(r.body))
	}
}

func TestThePluginGetsPlainJSONAndCaps(t *testing.T) {
	p := &testPlugin{}
	url := serve(t, p)
	r := plan(t, p, url, Answer{Data: map[string]any{"n": 2}, NotRead: []NotRead{{"a", "b"}}, Warnings: []string{"w"}}, nil)
	if string(r.body) != `{"ok":true,"data":{"n":2},"notRead":[{"what":"a","why":"b"}],"warnings":["w"]}` {
		t.Error(string(r.body))
	}
	want := []any{[]any{"Plan", map[string]any{"k": int64(1)}, Caps{60, 1048576}}}
	if !reflect.DeepEqual(p.calls, want) {
		t.Errorf("%#v", p.calls)
	}
}

func TestNetworkReachesThePluginWithItsDefaultsFilledIn(t *testing.T) {
	for _, c := range []struct {
		net  map[string]any
		seen Network
	}{
		{map[string]any{}, Network{Hosts: []string{}}},
		{map[string]any{"hosts": []string{"a.com"}}, Network{Hosts: []string{"a.com"}}},
		{map[string]any{"replay": map[string]any{"start": "2026-10-31T12:00:00Z"}},
			Network{Hosts: []string{}, Replay: &Replay{Start: "2026-10-31T12:00:00Z", Exchanges: map[string][]any{}}}},
	} {
		p := &testPlugin{}
		r := post(t, serve(t, p), "RunUnit", map[string]any{"caps": caps, "input": map[string]any{}, "network": c.net})
		if r.status != 200 || len(p.calls) != 1 || !reflect.DeepEqual(p.calls[0].([]any)[2], c.seen) {
			t.Errorf("%v: %d %#v", c.net, r.status, p.calls)
		}
	}
}

func TestARefusalCarriesItsEnvelopeAsAFailureDetail(t *testing.T) {
	p := &testPlugin{}
	url := serve(t, p)
	for _, err := range []error{
		Refusal{Code: connect.CodeUnavailable, Warnings: []string{"later", "really"}, NotRead: []NotRead{{"x", "y"}}},
		&Refusal{Code: connect.CodeUnavailable, Warnings: []string{"later", "really"}, NotRead: []NotRead{{"x", "y"}}},
		fmt.Errorf("wrapped: %w", Refusal{Code: connect.CodeUnavailable, Warnings: []string{"later", "really"}, NotRead: []NotRead{{"x", "y"}}}),
	} {
		e, f := failureOf(t, plan(t, p, url, Answer{}, err), 503, "unavailable")
		got, _ := protojson.Marshal(f)
		var env map[string]any
		_ = json.Unmarshal(got, &env)
		want := map[string]any{"data": nil, "notRead": []any{map[string]any{"what": "x", "why": "y"}}, "warnings": []any{"later", "really"}}
		if e.Message != "later; really" || !reflect.DeepEqual(env, want) {
			t.Errorf("%q %v", e.Message, env)
		}
	}
}

func size(e *connect.Error) int { return len(errorBody(e)) }

func TestAnErrorFitsOutputBytesBySayingLessNeverByDroppingItsDetail(t *testing.T) {
	// at the smallest outputBytes the contract allows, with warnings it allows (up to 500 characters)
	for _, c := range []struct {
		warnings []string
		notRead  []NotRead
	}{{[]string{strings.Repeat("w", 420)}, nil}, {[]string{strings.Repeat("é", 300)}, nil},
		{[]string{strings.Repeat("a", 500), strings.Repeat("b", 500)}, []NotRead{{strings.Repeat("x", 300), strings.Repeat("y", 300)}}}} {
		e := failure(connect.CodeUnavailable, c.warnings, c.notRead, 1024)
		if len(e.Details()) != 1 || e.Message() == "" || !strings.HasPrefix(c.warnings[0], e.Message()) || size(e) > 1024 {
			t.Errorf("%.3s: %d details, %q, %d bytes", c.warnings[0], len(e.Details()), e.Message(), size(e))
		}
	}
}

func TestAnErrorSaysLessInOrderNotReadThenLaterWarningsThenTheFirstOnesEnd(t *testing.T) {
	w, nr := []string{"one", "two"}, []NotRead{{"qq", "zz"}}
	full := failure(connect.CodeUnavailable, w, nr, 0)
	if got := failure(connect.CodeUnavailable, w, nr, size(full)); !bytes.Equal(errorBody(got), errorBody(full)) {
		t.Error("an error that fits at exactly its size is cut")
	}
	less := failure(connect.CodeUnavailable, w, nr, size(full)-1)
	if less.Message() != "one; two" || bytes.Contains(less.Details()[0].Value, []byte("qq")) {
		t.Error(less.Message(), less.Details()[0].Value)
	}
	if got := failure(connect.CodeUnavailable, w, nil, size(less)-1).Message(); got != "one" {
		t.Error(got)
	}
	one := failure(connect.CodeUnavailable, []string{"one"}, nil, 0)
	if got := failure(connect.CodeUnavailable, []string{"one"}, nil, size(one)-1).Message(); got != "on" {
		t.Error(got)
	}
}

func TestOnlyBelowTheContractsSmallestOutputBytesDoesAnErrorLoseItsDetail(t *testing.T) {
	e := failure(connect.CodeInvalidArgument, []string{"caps.outputBytes must be 1024 to 16777216, not 40"}, nil, 40)
	if e.Message() != "" || len(e.Details()) != 0 || size(e) > 40 {
		t.Error(e, len(e.Details()))
	}
	e = failure(connect.CodeInvalidArgument, []string{"caps.outputBytes must be 1024 to 16777216, not 100"}, nil, 100)
	if size(e) > 100 {
		t.Error(e, size(e))
	}
}

func TestTheFirstWarningsPrefixFitsExactlyAtTheCap(t *testing.T) {
	warning := strings.Repeat("x", 600)
	for _, k := range []int{1, 37, 499} {
		limit := size(failure(connect.CodeInternal, []string{warning[:k]}, nil, 0))
		if got := failure(connect.CodeInternal, []string{warning}, nil, limit).Message(); got != warning[:k] {
			t.Errorf("%d: %d characters", k, len(got))
		}
		if got := failure(connect.CodeInternal, []string{warning}, nil, limit-1).Message(); got != warning[:k-1] {
			t.Errorf("%d - 1: %d characters", k, len(got))
		}
	}
}

func TestAWarningLongerThanTheEnvelopeAllowsIsCutTo500Characters(t *testing.T) {
	if got := failure(connect.CodeInternal, []string{strings.Repeat("é", 600)}, nil, 0).Message(); got != strings.Repeat("é", 500) {
		t.Error(len([]rune(got)))
	}
}

func TestABodyThatDoesNotDecodeGetsAnErrorThatFitsAnyLegalOutputBytes(t *testing.T) {
	// caps.outputBytes is inside the body that did not decode, so the error fits the smallest legal one
	url := serve(t, &testPlugin{})
	for _, key := range []string{strings.Repeat("k", 2000), strings.Repeat("\U0001F600", 600)} {
		body := []byte(`{"caps": {"timeSeconds": 60, "outputBytes": 1024}, "` + key + `": 1}`)
		r := post(t, url, "Describe", body)
		failureOf(t, r, 400, "invalid_argument")
		if len(r.body) > 1024 {
			t.Errorf("%.3s: %d bytes", key, len(r.body))
		}
	}
}

func TestADecodingErrorIsFittedToExactlyTheSmallestLegalOutputBytes(t *testing.T) {
	// a body whose whole decoding error is 1025 bytes: fitted to 1024 it must say less, whatever
	// outputBytes the body names
	for n := 1; n < 600; n++ {
		body := []byte(`{"caps": {"timeSeconds": 60, "outputBytes": 4096}, "` + strings.Repeat("k", n) + `": 1}`)
		err := decode(body, new(pb.DescribeRequest))
		whole := failure(connect.CodeInvalidArgument, []string{"the body is not a valid DescribeRequest: " + err.Error()}, nil, 0)
		if size(whole) != 1025 {
			continue
		}
		r := post(t, serve(t, &testPlugin{}), "Describe", body)
		failureOf(t, r, 400, "invalid_argument")
		if len(r.body) > 1024 {
			t.Errorf("%d bytes", len(r.body))
		}
		return
	}
	t.Fatal("no key length makes a 1025-byte error")
}

func TestANumberThatOverflowsToInfinityIsInvalidArgumentWithItsFailure(t *testing.T) {
	url := serve(t, &testPlugin{})
	huge := "1" + strings.Repeat("0", 400)
	for _, input := range []string{`{"a": -1e400}`, `{"a": [1, 1e400]}`, `{"a": {"b": 1E999}}`, `{"a": ` + huge + `}`, `{"a": [-` + huge + `]}`,
		`{"a": ` + edge().String() + `}`, `{"a": -` + edge().String() + `}`} {
		failureOf(t, post(t, url, "Plan", []byte(`{`+capsJSON+`, "input": `+input+`}`)), 400, "invalid_argument")
	}
}

func TestAnIntegerThatRoundsToAFiniteDoubleIsAccepted(t *testing.T) {
	p := &testPlugin{answer: answers(Answer{Data: map[string]any{}}, nil)}
	url := serve(t, p)
	below := new(big.Int).Sub(edge(), big.NewInt(1)).String()
	for _, n := range []string{fmt.Sprintf("%.0f", math.MaxFloat64), below, "-" + below} {
		if r := post(t, url, "Plan", []byte(`{`+capsJSON+`, "input": {"a": `+n+`}}`)); r.status != 200 {
			t.Errorf("%.8s...: %d %s", n, r.status, r.body)
		}
	}
}

func TestEveryErrorTheRuntimeAnswersCarriesAFailure(t *testing.T) {
	p := &testPlugin{answer: func(context.Context, map[string]any, Caps) (Answer, error) {
		time.Sleep(2 * time.Second)
		return Answer{}, nil
	}}
	url := serve(t, p)
	short := map[string]any{"timeSeconds": 1, "outputBytes": 1048576}
	for _, c := range []struct {
		call   string
		body   any
		status int
		code   string
	}{
		{"Merge", map[string]any{"caps": caps, "input": map[string]any{}}, 501, "unimplemented"},
		{"Plan", map[string]any{"caps": short, "input": map[string]any{}}, 504, "deadline_exceeded"},
		{"Describe", map[string]any{"caps": map[string]any{"timeSeconds": 60, "outputBytes": 1023}}, 400, "invalid_argument"},
		{"Describe", []byte(`{"caps": {"timeSeconds": 1, "timeSeconds": 2}}`), 400, "invalid_argument"},
		{"Describe", []byte(`{"caps": {"retries": 1}}`), 400, "invalid_argument"},
		{"Describe", []byte(``), 400, "invalid_argument"},
	} {
		failureOf(t, post(t, url, c.call, c.body), c.status, c.code)
	}
}

func TestThePluginGetsIntegralNumbersAsIntegers(t *testing.T) {
	p := &testPlugin{answer: answers(Answer{Data: map[string]any{}}, nil)}
	url := serve(t, p)
	post(t, url, "Plan", []byte(`{`+capsJSON+`, "input": {"n": 5, "l": [1, 2.5, 3.0], "big": 9007199254740994}}`))
	got := p.calls[0].([]any)[1].(map[string]any)
	want := map[string]any{"n": int64(5), "l": []any{int64(1), 2.5, int64(3)}, "big": float64(9007199254740994)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%#v", got)
	}
	post(t, url, "RunUnit", map[string]any{"caps": caps, "input": map[string]any{}, "network": map[string]any{"replay": map[string]any{
		"start": "2026-10-31T12:00:00Z", "exchanges": map[string]any{"https://a.com/": []any{map[string]any{"status": 200}}}}}})
	net := p.calls[1].([]any)[2].(Network)
	if status := net.Replay.Exchanges["https://a.com/"][0].(map[string]any)["status"]; status != int64(200) {
		t.Errorf("%#v", status)
	}
}

func TestAnAnswerOrRefusalOutsideTheEnvelopesLimitsIsInternal(t *testing.T) {
	p := &testPlugin{}
	url := serve(t, p)
	unavailable := func(w []string, nr []NotRead) error {
		return Refusal{Code: connect.CodeUnavailable, Warnings: w, NotRead: nr}
	}
	for _, c := range []struct {
		a   Answer
		err error
	}{
		{Answer{Warnings: []string{""}}, nil}, {Answer{Warnings: []string{strings.Repeat("w", 501)}}, nil},
		{Answer{NotRead: []NotRead{{"", "y"}}}, nil}, {Answer{NotRead: []NotRead{{"x", strings.Repeat("y", 301)}}}, nil},
		{Answer{}, unavailable([]string{strings.Repeat("w", 501)}, nil)}, {Answer{}, unavailable([]string{"w"}, []NotRead{{"x", ""}})},
		{Answer{}, unavailable(nil, nil)}, {Answer{}, unavailable([]string{""}, nil)},
		{Answer{}, Refusal{Code: connect.CodeInternal, Warnings: []string{"x"}}},
		{Answer{}, Refusal{Code: connect.CodeDeadlineExceeded, Warnings: []string{"x"}}},
		{Answer{}, (*Refusal)(nil)},
	} {
		failureOf(t, plan(t, p, url, c.a, c.err), 500, "internal")
	}
	ok := Answer{Data: map[string]any{}, Warnings: []string{strings.Repeat("é", 500)}, NotRead: []NotRead{{strings.Repeat("x", 300), strings.Repeat("y", 300)}}}
	if r := plan(t, p, url, ok, nil); r.status != 200 {
		t.Error(r.status, string(r.body))
	}
	failureOf(t, plan(t, p, url, Answer{}, Refusal{Code: connect.CodeFailedPrecondition, Warnings: []string{strings.Repeat("é", 500)}}), 400, "failed_precondition")
}

func TestDescribeDataThatIsNotAnObjectIsInternal(t *testing.T) {
	p := &testPlugin{describe: func() (Answer, error) { return Answer{Data: []any{"not", "an", "object"}}, nil }}
	failureOf(t, post(t, serve(t, p), "Describe", map[string]any{"caps": caps}), 500, "internal")
}

func TestAnAnswerOfExactlyOutputBytesPassesAndOneByteMoreFails(t *testing.T) {
	p := &testPlugin{}
	url := serve(t, p)
	data := map[string]any{"s": strings.Repeat("x", 1500)}
	n := len(envelope(data, nil, nil))
	if r := plan(t, p, url, Answer{Data: data}, nil, "outputBytes", n); r.status != 200 || len(r.body) != n {
		t.Error(r.status, len(r.body), n)
	}
	failureOf(t, plan(t, p, url, Answer{Data: data}, nil, "outputBytes", n-1), 429, "resource_exhausted")
}

func TestTheBodySizeIsWhatIsSent(t *testing.T) {
	p := &testPlugin{}
	warning := strings.Repeat("no ", 20)
	r := plan(t, p, serve(t, p), Answer{}, Refusal{Code: connect.CodeFailedPrecondition, Warnings: []string{warning}})
	if want := errorBody(failure(connect.CodeFailedPrecondition, []string{warning}, nil, 1048576)); !bytes.Equal(r.body, want) {
		t.Errorf("%s\n%s", r.body, want)
	}
}

func TestAnAnswerOverOutputBytesIsResourceExhausted(t *testing.T) {
	p := &testPlugin{}
	url := serve(t, p)
	r := plan(t, p, url, Answer{Data: map[string]any{"s": strings.Repeat("x", 2000)}}, nil, "outputBytes", 1024)
	failureOf(t, r, 429, "resource_exhausted")
	if len(r.body) > 1024 {
		t.Error(len(r.body))
	}
	if r := plan(t, p, url, Answer{Data: map[string]any{"s": strings.Repeat("x", 900)}}, nil, "outputBytes", 1024); r.status != 200 || len(r.body) > 1024 {
		t.Error(r.status, len(r.body))
	}
}

func TestTheDeadlineIsTheSoonerOfTimeSecondsAndConnectTimeoutMs(t *testing.T) {
	deaf := func(context.Context, map[string]any, Caps) (Answer, error) { // ignores ctx (machinery-python#17)
		time.Sleep(3 * time.Second)
		return Answer{}, nil
	}
	p := &testPlugin{answer: deaf}
	url := serve(t, p)
	started := time.Now()
	failureOf(t, post(t, url, "Plan", map[string]any{"caps": map[string]any{"timeSeconds": 1, "outputBytes": 1048576}, "input": map[string]any{}}), 504, "deadline_exceeded")
	if d := time.Since(started); d < 900*time.Millisecond || d > 2500*time.Millisecond {
		t.Error(d)
	}
	started = time.Now()
	failureOf(t, post(t, url, "Plan", map[string]any{"caps": caps, "input": map[string]any{}}, "Connect-Timeout-Ms", "300"), 504, "deadline_exceeded")
	if d := time.Since(started); d < 250*time.Millisecond || d > 1500*time.Millisecond {
		t.Error(d)
	}
}

func TestAPluginThatHonoursCtxSeesItEndAtTheDeadline(t *testing.T) {
	ended := make(chan error, 1)
	p := &testPlugin{answer: func(ctx context.Context, _ map[string]any, _ Caps) (Answer, error) {
		<-ctx.Done()
		ended <- ctx.Err()
		return Answer{}, ctx.Err()
	}}
	failureOf(t, post(t, serve(t, p), "Plan", map[string]any{"caps": caps, "input": map[string]any{}}, "Connect-Timeout-Ms", "200"), 504, "deadline_exceeded")
	select {
	case err := <-ended:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		t.Error("the plugin's ctx did not end")
	}
}

func TestAPluginThatBreaksIsInternalNeverASuccess(t *testing.T) {
	p := &testPlugin{}
	url := serve(t, p)
	for _, c := range []struct {
		a   Answer
		err error
	}{{Answer{}, errors.New("boom")}, {Answer{Data: math.NaN()}, nil}, {Answer{Data: map[string]any{"c": make(chan int)}}, nil},
		{Answer{Data: []any{math.Inf(-1)}}, nil}} {
		failureOf(t, plan(t, p, url, c.a, c.err), 500, "internal")
	}
	p.answer = func(context.Context, map[string]any, Caps) (Answer, error) { panic("boom") }
	failureOf(t, post(t, url, "Plan", map[string]any{"caps": caps, "input": map[string]any{}}), 500, "internal")
}

func TestACallWithoutAMethodIsUnimplemented(t *testing.T) {
	failureOf(t, post(t, serve(t, &testPlugin{}), "Merge", map[string]any{"caps": caps, "input": map[string]any{}}), 501, "unimplemented")
}

func TestOnlyTheFiveCallsAndOnlyJSONAreServed(t *testing.T) {
	url := serve(t, &testPlugin{})
	if r := post(t, url, "Describe", []byte("\x0a\x00"), "Content-Type", "application/proto"); r.status != 415 {
		t.Error(r.status)
	}
	if r := post(t, url, "Describe", map[string]any{"caps": caps}, "Content-Type", "application/json; charset=utf-8"); r.status != 200 {
		t.Error(r.status)
	}
	failureOf(t, post(t, url, "Describe", map[string]any{"caps": caps}, "Content-Encoding", "gzip"), 501, "unimplemented")
	failureOf(t, post(t, url, "Describe", map[string]any{"caps": caps}, "Connect-Timeout-Ms", "1.5"), 400, "invalid_argument")
	for _, call := range []string{"FetchEverything", "../Describe"} {
		if r := post(t, url, call, map[string]any{"caps": caps}); r.status != 404 {
			t.Error(call, r.status)
		}
	}
	resp, err := http.Get(url + svc + "Describe")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Error(resp.StatusCode)
	}
}

func TestAnswersAreNeverCompressedSoTheirSizeIsWhatIsCounted(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, serve(t, &testPlugin{})+svc+"Describe", strings.NewReader(`{"caps": {"timeSeconds": 60, "outputBytes": 1048576}}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept-Encoding", "gzip")
	resp, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if enc := resp.Header.Get("Content-Encoding"); (enc != "" && enc != "identity") || !bytes.HasPrefix(body, []byte(`{"ok":true`)) {
		t.Error(enc, string(body))
	}
}

func TestARuleBrokenIsInvalidArgumentWithTheRule(t *testing.T) {
	e, _ := failureOf(t, post(t, serve(t, &testPlugin{}), "Describe", map[string]any{"caps": map[string]any{"timeSeconds": 60, "outputBytes": 1023}}), 400, "invalid_argument")
	if !strings.HasPrefix(e.Message, "caps.outputBytes must be 1024 to 16777216") {
		t.Error(e.Message)
	}
}

func TestServeBindsTo127001AndPrintsItsURLFirst(t *testing.T) {
	pr, pw := io.Pipe()
	go func() { _ = Serve(&testPlugin{}, Options{Stdout: pw}) }()
	line, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "http://127.0.0.1:") {
		t.Fatalf("%q %v", line, err)
	}
	if r := post(t, strings.TrimSpace(line), "Describe", map[string]any{"caps": caps}); r.status != 200 {
		t.Error(r.status)
	}
}

// stdioCall answers one framed call and splits the status line from the body.
func stdioCall(t *testing.T, p any, in string) (connect.Code, string, []byte) {
	t.Helper()
	var out bytes.Buffer
	code := stdio(context.Background(), p, strings.NewReader(in), &out)
	line, body, ok := bytes.Cut(out.Bytes(), []byte("\n"))
	if !ok {
		t.Fatalf("no status line: %q", out.String())
	}
	var status struct{ Status string }
	if err := json.Unmarshal(line, &status); err != nil {
		t.Fatal(err)
	}
	return code, status.Status, body
}

func frame(call string, ms int) string {
	return fmt.Sprintf(`{"path": %q, "timeoutMs": %d}`+"\n", svc+call, ms)
}

func TestStdioAnswersOneCallFramedAsTheBindingSays(t *testing.T) {
	p := &testPlugin{}
	code, status, body := stdioCall(t, p, frame("Describe", 60000)+`{"caps": {"timeSeconds": 60, "outputBytes": 1048576}}`)
	if code != 0 || status != "ok" || !bytes.HasPrefix(body, []byte(`{"ok":true,"data":{"contracts":["machinery.plugin.v1alpha1"]`)) {
		t.Errorf("%v %s %s", code, status, body)
	}
	p.answer = answers(Answer{}, Refusal{Code: connect.CodeUnavailable, Warnings: []string{"later"}})
	code, status, body = stdioCall(t, p, frame("Plan", 60000)+`{`+capsJSON+`, "input": {}}`)
	if code != connect.CodeUnavailable || status != "unavailable" {
		t.Error(code, status)
	}
	failureOf(t, reply{503, "application/json", body}, 503, "unavailable")
	code, status, body = stdioCall(t, p, frame("FetchEverything", 60000)+`{`+capsJSON+`}`)
	if code != connect.CodeUnimplemented || status != "unimplemented" || bytes.Contains(body, []byte("details")) {
		t.Errorf("%v %s %s", code, status, body)
	}
	failureOf(t, reply{501, "", stdioOnly(stdioCall(t, p, frame("Merge", 60000)+`{`+capsJSON+`, "input": {}}`))}, 501, "unimplemented")
}

func stdioOnly(_ connect.Code, _ string, body []byte) []byte { return body }

func TestStdioRefusesABadFrame(t *testing.T) {
	describe := `{"caps": {"timeSeconds": 60, "outputBytes": 1048576}}`
	for _, in := range []string{
		`{"path": "` + svc + `Describe"}` + "\n" + describe, `{"timeoutMs": 1000}` + "\n" + describe,
		`{"path": "` + svc + `Describe", "timeoutMs": 1000, "x": 1}` + "\n" + describe,
		`{"path": "` + svc + `Describe", "timeoutMs": -1}` + "\n" + describe,
		`{"path": "` + svc + `Describe", "timeoutMs": 1.5}` + "\n" + describe,
		`{"path": "` + svc + `Describe", "path": "` + svc + `Describe", "timeoutMs": 1000}` + "\n" + describe,
		`{"path": "` + svc + `Describe", "timeoutMs": 1000}`, `not json` + "\n" + describe, "",
	} {
		code, status, body := stdioCall(t, &testPlugin{}, in)
		if code != connect.CodeInvalidArgument || status != "invalid_argument" {
			t.Errorf("%q: %v %s", in, code, status)
		}
		failureOf(t, reply{400, "", body}, 400, "invalid_argument")
	}
}

func TestStdioTimeoutMsShortensTheDeadline(t *testing.T) {
	p := &testPlugin{answer: func(context.Context, map[string]any, Caps) (Answer, error) {
		time.Sleep(3 * time.Second)
		return Answer{}, nil
	}}
	started := time.Now()
	code, status, _ := stdioCall(t, p, frame("Plan", 300)+`{`+capsJSON+`, "input": {}}`)
	if code != connect.CodeDeadlineExceeded || status != "deadline_exceeded" || time.Since(started) > 1500*time.Millisecond {
		t.Error(code, status, time.Since(started))
	}
}

func TestServeWithStdioReturnsTheCodeItAnswered(t *testing.T) {
	var out bytes.Buffer
	if err := Serve(&testPlugin{}, Options{Stdio: true, Stdin: strings.NewReader(frame("Describe", 1000) + `{` + capsJSON + `}`), Stdout: &out}); err != nil {
		t.Error(err)
	}
	err := Serve(&testPlugin{}, Options{Stdio: true, Stdin: strings.NewReader(frame("Merge", 1000) + `{` + capsJSON + `, "input": {}}`), Stdout: &out})
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Error(err)
	}
}
