package runtime

// The rules of contract v1alpha1's request.schema.json that proto cannot state. They are checked on the
// decoded request, so a value left out is its default, and the first rule broken is the answer
// (invalid_argument).

import (
	"fmt"
	"maps"
	"math"
	neturl "net/url"
	"regexp"
	"slices"

	pb "github.com/practice-library/machinery/gen/go/machinery/plugin/v1alpha1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

var (
	timeSeconds = [2]uint32{1, 3600}
	outputBytes = [2]uint32{1024, 16777216}
	statusRange = [2]int64{100, 599}
	hostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	startUTC    = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`)
	recordedKey = map[string]bool{"status": true, "headers": true, "body": true, "error": true}
)

type (
	withCaps    interface{ GetCaps() *pb.Caps }
	withInput   interface{ GetInput() *structpb.Struct }
	withNetwork interface{ GetNetwork() *pb.Network }
)

// check says why req, a request of call, breaks a rule, or "".
func check(call string, req proto.Message) string {
	caps := req.(withCaps).GetCaps()
	if caps == nil {
		return "caps is required"
	}
	for _, c := range []struct {
		name  string
		value uint32
		r     [2]uint32
	}{{"timeSeconds", caps.GetTimeSeconds(), timeSeconds}, {"outputBytes", caps.GetOutputBytes(), outputBytes}} {
		if c.value < c.r[0] || c.value > c.r[1] {
			return fmt.Sprintf("caps.%s must be %d to %d, not %d", c.name, c.r[0], c.r[1], c.value)
		}
	}
	if r, ok := req.(withInput); ok && r.GetInput() == nil {
		return "input is required"
	}
	if r, ok := req.(withNetwork); ok {
		if r.GetNetwork() == nil {
			return "network is required on " + call
		}
		return network(r.GetNetwork())
	}
	return ""
}

func network(net *pb.Network) string {
	for _, host := range net.GetHosts() {
		if !hostPattern.MatchString(host) {
			return fmt.Sprintf("network.hosts: %s is not a lower-case host name", shown(host, 80))
		}
	}
	replay := net.GetReplay()
	if replay == nil {
		return ""
	}
	if len(net.GetHosts()) > 0 {
		return "network has both hosts and replay: replay goes with an empty hosts"
	}
	if !startUTC.MatchString(replay.GetStart()) {
		return fmt.Sprintf("network.replay.start: %s is not a UTC time as 2026-10-31T12:00:00Z", shown(replay.GetStart(), 40))
	}
	exchanges := replay.GetExchanges()
	for n, url := range slices.Sorted(maps.Keys(exchanges)) {
		for i, rec := range fromList(exchanges[url]) {
			if why := recorded(rec); why != "" {
				return fmt.Sprintf("network.replay.exchanges: url %d of %d (%s), response %d: %s", n+1, len(exchanges), redact(url), i, why)
			}
		}
	}
	return ""
}

func recorded(v any) string {
	rec, ok := v.(map[string]any)
	if !ok {
		return "a recorded response is an object"
	}
	for _, k := range slices.Sorted(maps.Keys(rec)) {
		if !recordedKey[k] {
			return fmt.Sprintf("unknown field %s", shown(k, 40))
		}
	}
	if e, ok := rec["error"]; ok {
		if len(rec) != 1 {
			return "error stands alone: no status, headers or body with it"
		}
		if s, ok := e.(string); !ok || s == "" {
			return "error is a non-empty string"
		}
		return ""
	}
	status, ok := rec["status"]
	if !ok || status == nil {
		return "a recorded response has a status or an error"
	}
	// fromValue makes an integral number an int64, so a fraction, a bool or a string is not one
	if n, ok := status.(int64); !ok || n < statusRange[0] || n > statusRange[1] {
		return fmt.Sprintf("status must be an integer from %d to %d, not %s", statusRange[0], statusRange[1], kind(status))
	}
	if headers, ok := rec["headers"]; ok {
		h, ok := headers.(map[string]any)
		if !ok {
			return "headers map names to strings"
		}
		for _, value := range h {
			if _, ok := value.(string); !ok {
				return "headers map names to strings"
			}
		}
	}
	if body, ok := rec["body"]; ok {
		if _, ok := body.(string); !ok {
			return "body is a string"
		}
	}
	return ""
}

// redact names a URL by its scheme and host only: never its userinfo, path, query or fragment, which can
// hold credentials or tokens.
func redact(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil || u.Scheme == "" || u.Hostname() == "" || !hostname.MatchString(u.Hostname()) {
		return "not a URL with a host"
	}
	return u.Scheme + "://" + u.Hostname()
}

var hostname = regexp.MustCompile(`\A[a-z0-9.-]{1,253}\z`)

// kind names a JSON value's type and never its value: an integer the rule refuses is only out of range.
func kind(v any) string {
	switch x := v.(type) {
	case int64:
		return "an integer out of range"
	case float64:
		if x == math.Trunc(x) { // integral beyond 2^53, held as a double
			return "an integer out of range"
		}
		return "a number with a fraction"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	}
	return "null"
}

// shown is how a diagnostic names a value it was given: in double quotes when it is wholly
// [A-Za-z0-9_.:+-] and at most n characters, which then need no escaping and read the same in every
// runtime; otherwise only as "a value with other characters", so no text the caller or plugin chose is
// copied into an error.
func shown(s string, n int) string {
	if len(s) <= n && plainValue.MatchString(s) {
		return `"` + s + `"`
	}
	return "a value with other characters"
}

var plainValue = regexp.MustCompile(`\A[A-Za-z0-9_.:+-]+\z`)

// cut is s's first n characters.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
