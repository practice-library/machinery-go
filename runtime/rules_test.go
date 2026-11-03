package runtime

import (
	"strings"
	"testing"

	pb "github.com/practice-library/machinery/gen/go/machinery/plugin/v1alpha1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const capsJSON = `"caps": {"timeSeconds": 60, "outputBytes": 1048576}`

func request(t *testing.T, call, body string) proto.Message {
	t.Helper()
	msg := map[string]proto.Message{"Describe": new(pb.DescribeRequest), "Plan": new(pb.PlanRequest),
		"RunUnit": new(pb.RunUnitRequest), "Merge": new(pb.MergeRequest), "Run": new(pb.RunRequest)}[call]
	if err := protojson.Unmarshal([]byte(body), msg); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return msg
}

func TestCapsBounds(t *testing.T) {
	for caps, ok := range map[string]bool{
		`{"timeSeconds": 1, "outputBytes": 1024}`: true, `{"timeSeconds": 3600, "outputBytes": 16777216}`: true,
		`{"timeSeconds": 0, "outputBytes": 1024}`: false, `{"timeSeconds": 3601, "outputBytes": 1024}`: false,
		`{"timeSeconds": 1, "outputBytes": 1023}`: false, `{"timeSeconds": 1, "outputBytes": 16777217}`: false,
		`{"outputBytes": 1024}`: false, `{"timeSeconds": 1}`: false,
	} {
		if why := check("Describe", request(t, "Describe", `{"caps": `+caps+`}`)); (why == "") != ok {
			t.Errorf("%s: %q", caps, why)
		}
	}
	if why := check("Describe", request(t, "Describe", `{}`)); why != "caps is required" {
		t.Error(why)
	}
}

func TestInputOnEveryCallButDescribeAndNetworkOnRunUnitAndRun(t *testing.T) {
	if why := check("Describe", request(t, "Describe", `{`+capsJSON+`}`)); why != "" {
		t.Error(why)
	}
	for _, call := range []string{"Plan", "Merge", "RunUnit", "Run"} {
		if why := check(call, request(t, call, `{`+capsJSON+`}`)); why != "input is required" {
			t.Error(call, why)
		}
	}
	for _, call := range []string{"RunUnit", "Run"} {
		if why := check(call, request(t, call, `{`+capsJSON+`, "input": {}}`)); why != "network is required on "+call {
			t.Error(call, why)
		}
		if why := check(call, request(t, call, `{`+capsJSON+`, "input": {}, "network": {}}`)); why != "" {
			t.Error(call, why)
		}
	}
	if why := check("Plan", request(t, "Plan", `{`+capsJSON+`, "input": {}}`)); why != "" {
		t.Error(why)
	}
}

func networkOf(t *testing.T, body string) string {
	t.Helper()
	net := new(pb.Network)
	if err := protojson.Unmarshal([]byte(body), net); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return network(net)
}

func TestHosts(t *testing.T) {
	for host, ok := range map[string]bool{"docs.example.com": true, "a-b.c0": true, "Docs.example.com": false,
		"localhost": false, "-a.com": false, "a-.com": false, "a..com": false, `docs.example.com\n`: false,
		"dоcs.example.com": false, "docs.example.com.": false, "x.docs.example.com": true} {
		if why := networkOf(t, `{"hosts": ["`+host+`"]}`); (why == "") != ok {
			t.Errorf("%q: %q", host, why)
		}
	}
}

func TestReplayGoesWithAnEmptyHostsAndAStart(t *testing.T) {
	start := "2026-10-31T12:00:00Z"
	if why := networkOf(t, `{"hosts": [], "replay": {"start": "`+start+`"}}`); why != "" {
		t.Error(why)
	}
	if why := networkOf(t, `{"hosts": ["a.com"], "replay": {"start": "`+start+`"}}`); !strings.Contains(why, "both hosts and replay") {
		t.Error(why)
	}
	for _, bad := range []string{"", "2026-10-31T12:00:00", "2026-10-31T12:00:00.5Z", "2026-10-31 12:00:00Z", start + `\n`, "x" + start} {
		if why := networkOf(t, `{"replay": {"start": "`+bad+`"}}`); !strings.Contains(why, "replay.start") {
			t.Errorf("%q: %q", bad, why)
		}
	}
}

func TestARecordedResponse(t *testing.T) {
	for rec, ok := range map[string]bool{`{"status": 200.0}`: true, `{"status": 100}`: true, `{"status": 599}`: true,
		`{"status": 200, "headers": {"a": "b"}, "body": "x"}`: true, `{"error": "reset"}`: true,
		`{"status": 99}`: false, `{"status": 600}`: false, `{"status": 200.5}`: false, `{"status": true}`: false,
		`{"status": "200"}`: false, `{}`: false, `{"body": "x"}`: false, `{"status": null}`: false,
		`{"error": ""}`: false, `{"error": 1}`: false, `{"error": "x", "status": 200}`: false, `{"error": "x", "body": ""}`: false,
		`{"status": 200, "headers": {"a": 1}}`: false, `{"status": 200, "headers": []}`: false, `{"status": 200, "body": 1}`: false,
		`{"status": 200, "delay": 1}`: false, `200`: false, `[]`: false} {
		body := `{"replay": {"start": "2026-10-31T12:00:00Z", "exchanges": {"https://a.com/": [{"status": 200}, ` + rec + `]}}}`
		if why := networkOf(t, body); (why == "") != ok {
			t.Errorf("%s: %q", rec, why)
		}
	}
	for _, rec := range []string{`200`, `[]`, `"x"`, `null`} {
		body := `{"replay": {"start": "2026-10-31T12:00:00Z", "exchanges": {"https://a.com/": [` + rec + `]}}}`
		if why := networkOf(t, body); !strings.HasSuffix(why, "a recorded response is an object") {
			t.Errorf("%s: %q", rec, why)
		}
	}
}

// A diagnostic names the rule and the kind of value, never the value: a recorded status can be any JSON,
// and a replay URL can hold credentials or tokens in its userinfo, path or query.
func TestDiagnosticsCopyNoValues(t *testing.T) {
	const secret = "s3cr3t"
	url := "https://user:" + secret + "@a.com/p/" + secret + "?token=" + secret + "#" + secret
	for rec, kindSaid := range map[string]string{
		`{"status": "` + secret + `\u0007\u001b[2J"}`: "a string",
		`{"status": {"x": "` + secret + `"}}`:         "an object",
		`{"status": ["` + secret + `"]}`:              "an array",
		`{"status": true}`:                            "a boolean",
		`{"status": 200.5}`:                           "a number with a fraction",
		`{"status": 99}`:                              "an integer out of range",
		`{"status": 1e20}`:                            "an integer out of range",
	} {
		body := `{"replay": {"start": "2026-10-31T12:00:00Z", "exchanges": {"` + url + `": [` + rec + `]}}}`
		why := networkOf(t, body)
		if !strings.Contains(why, "status must be") || !strings.HasSuffix(why, ", not "+kindSaid) {
			t.Errorf("%s: %q does not name the rule and %q", rec, why, kindSaid)
		}
		if strings.Contains(why, secret) || strings.ContainsAny(why, "\a\x1b") {
			t.Errorf("%s: %q copies a value", rec, why)
		}
		if !strings.Contains(why, "url 1 of 1 (https://a.com)") {
			t.Errorf("%s: %q does not name the URL by scheme and host", rec, why)
		}
	}
	for raw, want := range map[string]string{"https://a.com/x?y=1": "https://a.com", "not a url": "not a URL with a host",
		"https://A.COM/": "not a URL with a host", "https://a.com‮/": "not a URL with a host", "mailto:x@a.com": "not a URL with a host"} {
		if got := redact(raw); got != want {
			t.Errorf("redact(%q) = %q, want %q", raw, got, want)
		}
	}
}

// A value the caller chose is shown only when it is plain [A-Za-z0-9_.:+-] text of at most n characters,
// in double quotes; anything else, including control characters, quotes, spaces, other scripts and long
// values, is named only as "a value with other characters".
func TestShownCopiesOnlyPlainValues(t *testing.T) {
	for in, want := range map[string]string{"a.com": `"a.com"`, "time_seconds": `"time_seconds"`, "1e400": `"1e400"`, "2026-10-31T12:00": `"2026-10-31T12:00"`,
		"": "a value with other characters", "a b": "a value with other characters", "a\x1b[2J": "a value with other characters",
		`a"b`: "a value with other characters", "é": "a value with other characters", strings.Repeat("a", 41): "a value with other characters"} {
		if got := shown(in, 40); got != want {
			t.Errorf("shown(%q) = %s, want %s", in, got, want)
		}
	}
	if got := shown(strings.Repeat("a", 80), 80); got != `"`+strings.Repeat("a", 80)+`"` {
		t.Errorf("an 80-character host: %s", got)
	}
}

// Every rule that names a value the caller chose names it through shown: a host, the replay start, an
// unknown field of a recorded response.
func TestRulesNameValuesThroughShown(t *testing.T) {
	for body, says := range map[string]string{
		`{"hosts": ["x\u0007y \"secret\""]}`:           "network.hosts: a value with other characters is not",
		`{"replay": {"start": "x\u0007y \"secret\""}}`: "network.replay.start: a value with other characters is not",
		`{"replay": {"start": "2026-10-31T12:00:00Z", "exchanges": {"https://a.com/": [{"x\u0007y \"secret\"": 1}]}}}`: "unknown field a value with other characters",
		`{"hosts": ["Upper.example"]}`: `network.hosts: "Upper.example" is not`,
	} {
		why := networkOf(t, body)
		if !strings.Contains(why, says) || strings.Contains(why, "secret") || strings.ContainsRune(why, '\a') {
			t.Errorf("%s: %q, want %q and no copied text", body, why, says)
		}
	}
}
