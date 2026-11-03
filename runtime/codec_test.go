package runtime

import (
	"math"
	"math/big"
	"reflect"
	"strings"
	"testing"

	pb "github.com/practice-library/machinery/gen/go/machinery/plugin/v1alpha1"
)

func TestStrictJSONRefusesAKeyTwiceNaNInfinityALoneSurrogateAndBadUTF8(t *testing.T) {
	for _, body := range []string{`{"a": 1, "a": 2}`, `{"a": {"b": [{"c": 1, "c": 1}]}}`, `{"a": NaN}`, `{"a": -Infinity}`,
		`{"a": "\ud800"}`, `{"a": "\udc00"}`, `{"a": "\ud800\u0041"}`, `{"a": "\ud800\ud800"}`, `{"a": "\ud800x"}`, "\xff{}", `{"a": "` + "\xed\xa0\x80" + `"}`,
		``, `{} {}`, `{"a": 1e400}`, `not json`} {
		if strictCheck([]byte(body)) == nil {
			t.Errorf("%q: accepted", body)
		}
	}
	for _, body := range []string{`{"a": {"b": [1, 2.5]}}`, `{"a": "\ud83d\ude00"}`, `{"a": "\\ud800"}`, `{"a": "\\\ud83d\ude00"}`, `{"a": "😀"}`,
		`{"a": 1e-400}`, `[{"a": 1}, {"a": 1}]`, ` {} `} {
		if err := strictCheck([]byte(body)); err != nil {
			t.Errorf("%q: %v", body, err)
		}
	}
}

func TestIntegralNumbersUpTo2To53BecomeIntegers(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want any
	}{{3, int64(3)}, {2.5, 2.5}, {math.Copysign(0, -1), int64(0)}, {1 << 53, int64(1 << 53)}, {-(1 << 53), int64(-(1 << 53))},
		{1<<53 + 2, float64(1<<53 + 2)}} {
		if got := number(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("number(%v) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestNumbersAreWrittenAsTheContractsDoubles(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
	}{{3.0, "3"}, {int64(3), "3"}, {math.Copysign(0, -1), "0"}, {0.5, "0.5"}, {float64(1 << 53), "9007199254740992"},
		{float64(1<<53 + 2), "9007199254740994"}, {int64(1<<53 + 2), "9007199254740994"}, {int64(1<<60 + 1), "1152921504606847000"},
		{1e20, "100000000000000000000"}, {1e300, "1" + strings.Repeat("0", 300)}, {-1e20, "-100000000000000000000"}, {1.5, "1.5"},
		{1.2345678901234567e+17, "123456789012345660"}, {1e21 + 0.5, "1000000000000000000000"}, {1e-300, "1e-300"},
		{1e-5, "1e-05"}, {1234567.5, "1234567.5"}, {-1234567.5, "-1234567.5"}, {0.0001, "0.0001"}, {0.00012, "0.00012"},
		{1.2345e-05, "1.2345e-05"}, {123456789012345.6, "123456789012345.6"},
		{5e-324, "5e-324"}, {4.9406564584124654e-324, "5e-324"}, {2.2250738585072014e-308, "2.2250738585072014e-308"},
		{9.999999999999999e-05, "9.999999999999999e-05"}, {-1e-05, "-1e-05"}, {uint8(7), "7"}, {float32(1.5), "1.5"}, {[]int{1, 2}, "[1,2]"},
		{struct {
			A int `json:"a"`
		}{4}, `{"a":4}`}, {map[string]any{"b": "<&>", "a": nil}, `{"a":null,"b":"<&>"}`}} {
		v, err := plain(c.in)
		if err != nil {
			t.Fatalf("%v: %v", c.in, err)
		}
		if got := string(appendJSON(nil, v)); got != c.want {
			t.Errorf("%#v written as %s, want %s", c.in, got, c.want)
		}
	}
	for _, bad := range []any{math.NaN(), math.Inf(1), float32(math.Inf(-1)), []any{math.NaN()}, map[string]any{"a": math.Inf(1)}, make(chan int)} {
		if _, err := plain(bad); err == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
}

func TestTheEnvelopeHasEveryFieldAndIntegers(t *testing.T) {
	data, _ := plain(map[string]any{"n": 3.0, "x": 0.5})
	if got := string(envelope(data, nil, nil)); got != `{"ok":true,"data":{"n":3,"x":0.5},"notRead":[],"warnings":[]}` {
		t.Error(got)
	}
	if got := string(envelope(nil, []NotRead{{"a", "b"}}, []string{"w"})); got != `{"ok":true,"data":null,"notRead":[{"what":"a","why":"b"}],"warnings":["w"]}` {
		t.Error(got)
	}
}

func TestDecodeRefusesUnknownFieldsAndKeepsProtosLeniency(t *testing.T) {
	for _, body := range []string{`{"caps": {"timeSeconds": 1, "timeSeconds": 2}}`, `{"caps": {"timeSeconds": NaN}}`,
		`{"caps": {"timeSeconds": 1, "outputBytes": 1024, "retries": 1}}`, `{"input": {}}`} {
		if decode([]byte(body), new(pb.DescribeRequest)) == nil {
			t.Errorf("%s: accepted", body)
		}
	}
	req := new(pb.DescribeRequest)
	if err := decode([]byte(`{"caps": {"time_seconds": "60", "output_bytes": 2048}}`), req); err != nil {
		t.Fatal(err)
	}
	if req.GetCaps().GetTimeSeconds() != 60 || req.GetCaps().GetOutputBytes() != 2048 {
		t.Error(req)
	}
	if err := decode([]byte(`{"caps": null}`), req); err != nil || req.GetCaps() != nil {
		t.Error("caps null is caps left out", err)
	}
}

// 2^1024 - 2^970 is the midpoint between the largest double and 2^1024: below it a literal rounds to the
// largest double, which is finite; from it on it rounds to infinity (machinery-python#9)
func edge() *big.Int {
	one := big.NewInt(1)
	return new(big.Int).Sub(new(big.Int).Lsh(one, 1024), new(big.Int).Lsh(one, 970))
}

func TestAnIntegerThatRoundsToAFiniteDoubleIsAcceptedAndOneThatRoundsToInfinityIsNot(t *testing.T) {
	below := new(big.Int).Sub(edge(), big.NewInt(1))
	for _, n := range []string{new(big.Int).Lsh(big.NewInt(1), 1023).String(), below.String(), "-" + below.String()} {
		if err := strictCheck([]byte(`{"a": ` + n + `}`)); err != nil {
			t.Errorf("%s...: %v", n[:8], err)
		}
	}
	for _, n := range []string{edge().String(), "-" + edge().String(), "1" + strings.Repeat("0", 400)} {
		if strictCheck([]byte(`{"a": `+n+`}`)) == nil {
			t.Errorf("%s...: accepted", n[:8])
		}
	}
}

// protojson v1.36.12 refuses these too; the strict pre-decode answers first, so the contract's rules hold
// whatever the library does.
func TestTheStrictPreDecodeAnswersBeforeProtojson(t *testing.T) {
	for body, says := range map[string]string{`{"caps": {"timeSeconds": 1, "timeSeconds": 2}}`: "given twice",
		`{"caps": {"timeSeconds": 1, "outputBytes": 1024}, "x": "\ud800"}`: "lone surrogate", `{"caps": {"timeSeconds": 1e400}}`: "finite double"} {
		if err := decode([]byte(body), new(pb.DescribeRequest)); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%s: %v", body, err)
		}
	}
}
