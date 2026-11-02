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
