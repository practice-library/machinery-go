// Command guards checks that each guard, removed, makes the tests fail.
//
// For every row it copies the module to a temporary directory, replaces the guard's code (which must occur
// exactly once, so a row cannot silently stop applying), runs go test ./... and expects it to fail.
//
//	go run ./tests/guards [-run <regexp of the guards' descriptions>]
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	C = "runtime/codec.go"
	R = "runtime/rules.go"
	F = "runtime/failure.go"
	D = "runtime/deadline.go"
	S = "runtime/serve.go"
)

// guards: what the guard keeps true, its file, its code, and the code without it.
var guards = [][4]string{
	{"integral numbers leave as integers", C, "if x == math.Trunc(x) { // every integral", "if false { // every integral"},
	{"-0 leaves as 0", C, "if x == 0 {\n\t\t\treturn append(b, '0')", "if false {\n\t\t\treturn append(b, '0')"},
	{"only numbers exact as doubles reach the plugin as int64", C, "if f == math.Trunc(f) && math.Abs(f) <= maxExact {", "if f == math.Trunc(f) {"},
	{"integral numbers reach the plugin as int64", C, "\t\treturn int64(f)\n", "\t\treturn f\n"},
	{"the strict pre-decode runs before protojson", C, "\tif err := strictCheck(body); err != nil {\n\t\treturn err\n\t}\n\treturn protojson", "\treturn protojson"},
	{"a key given twice is refused", C, "if seen[k] {", "if false {"},
	{"a number that is not a finite double is refused", C, "if _, err := strconv.ParseFloat(string(t), 64); err != nil {", "if false {"},
	{"a lone surrogate is refused", C, `return errors.New("a string holds a lone surrogate")`, "return nil"},
	{"a surrogate pair is accepted", C, "if r <= 0xDBFF && hex4(b, i+6)", "if false && r <= 0xDBFF && hex4(b, i+6)"},
	{"a body that is not UTF-8 is refused", C, "if !utf8.Valid(body) {", "if !utf8.Valid(body) && false {"},
	{"a body is one JSON value", C, "if _, err := dec.Token(); err != io.EOF {", "if false {"},
	{"unknown fields are refused", C, "DiscardUnknown: false", "DiscardUnknown: true"},
	{"data is always written", C, "\treturn append(b, \"null\"...)\n}", "\treturn b\n}"},
	{"strings are written without HTML escapes", C, "enc.SetEscapeHTML(false)", "enc.SetEscapeHTML(true)"},
	{"a number that is not finite is not JSON", C, "if math.IsNaN(x) || math.IsInf(x, 0) {", "if false {"},
	{"caps bounds hold", R, "if c.value < c.r[0] || c.value > c.r[1] {", "if c.value < c.r[0]-1 || c.value > c.r[1]+1 {"},
	{"caps is required", R, "if caps == nil {", "if false {"},
	{"input is required but on Describe", R, "ok && r.GetInput() == nil", "ok && r.GetInput() == nil && false"},
	{"network is required on RunUnit and Run", R, `return "network is required on " + call`, `return ""`},
	{"a host is matched from its start", R, "regexp.MustCompile(`^[a-z0-9]", "regexp.MustCompile(`[a-z0-9]"},
	{"a host is matched to its end", R, "[a-z0-9])?)+$`)", "[a-z0-9])?)+`)"},
	{"a listed host with replay is refused", R, "if len(net.GetHosts()) > 0 {", "if false {"},
	{"the replay start is matched whole", R, "[0-9]{2}Z$`)", "[0-9]{2}Z`)"},
	{"a recorded response is an object", R, "rec, ok := v.(map[string]any)\n\tif !ok {", "rec, ok := v.(map[string]any)\n\tif false && !ok {"},
	{"a recorded response has no unknown field", R, "if !recordedKey[k] {", "if false {"},
	{"a recorded error stands alone", R, "if len(rec) != 1 {", "if false {"},
	{"a recorded error is not empty", R, `!ok || s == ""`, `!ok || s == "" && false`},
	{"a recorded status is 100 to 599", R, "n < statusRange[0] || n > statusRange[1]", "n < statusRange[0]-1 || n > statusRange[1]+1"},
	{"recorded headers are strings", R, "if _, ok := value.(string); !ok {", "if _, ok := value.(string); false && !ok {"},
	{"a recorded body is a string", R, "if _, ok := body.(string); !ok {", "if _, ok := body.(string); false && !ok {"},
	{"a warning within the envelope's limits", F, "n < 1 || n > warningMax {", "n < 1 {"},
	{"a notRead entry within the envelope's limits", F, "n < 1 || n > notReadMax {", "n < 1 {"},
	{"a warning is cut to the envelope's 500 characters", F, "cutWarnings[i] = cut(w, warningMax)", "cutWarnings[i] = w"},
	{"an error drops notRead first to fit", F, "\t\te = parts(code, warnings, nil)\n", ""},
	{"an error fits at exactly outputBytes", F, "if limit > 0 && len(errorBody(e)) > limit {\n\t\te = parts", "if limit > 0 && len(errorBody(e)) >= limit {\n\t\te = parts"},
	{"an error keeps its detail by shortening its first warning", F, "if len(errorBody(parts(code, []string{string(first[:mid])}, nil))) <= limit {", "if false {"},
	{"the first warning's prefix fits exactly at the cap", F, "nil))) <= limit {", "nil))) < limit {"},
	{"a Failure detail carries no debug copy", F, "EncodeToString(d.Value)), '}')", "EncodeToString(d.Value)), `,\"debug\":{}}`...)"},
	{"a Failure detail's value is unpadded base64", F, "base64.RawStdEncoding.EncodeToString(d.Value)", "base64.StdEncoding.EncodeToString(d.Value)"},
	{"a Failure carries data null", F, "Data: structpb.NewNullValue(), ", "Data: (*structpb.Value)(nil), "},
	{"Connect-Timeout-Ms shortens the deadline", D, "d = min(d, max(timeout, 0))", "_ = timeout"},
	{"a plugin's panic is internal", D, "done <- outcome{err: panicked{v}}", "done <- outcome{}"},
	{"the deadline is answered whether or not the plugin returns", D, "case <-ctx.Done(): // the deadline", "case <-make(chan int): // the deadline"},
	{"a decoding error fits the smallest outputBytes", S, "nil, int(outputBytes[0]))\n\t}\n\tpcaps", "nil, 0)\n\t}\n\tpcaps"},
	{"a decoding error is fitted to exactly the smallest legal outputBytes", S, "nil, int(outputBytes[0]))\n\t}\n\tpcaps", "nil, int(outputBytes[0])+1)\n\t}\n\tpcaps"},
	{"the rules are checked", S, "if why := check(spec.name, req); why != \"\" {", "if why := check(spec.name, req); false && why != \"\" {"},
	{"unimplemented carries a Failure", S, `return fail(connect.CodeUnimplemented, "this plugin does not answer "+spec.name, nil, limit)`, `return connect.CodeUnimplemented, errorBody(connect.NewError(connect.CodeUnimplemented, "no")), true`},
	{"deadline_exceeded carries a Failure", S, "return fail(connect.CodeDeadlineExceeded, fmt.Sprintf(\"the call did not finish within %g seconds\", d.Seconds()), nil, limit)", "return connect.CodeDeadlineExceeded, errorBody(connect.NewError(connect.CodeDeadlineExceeded, \"late\")), true"},
	{"network reaches the plugin with its defaults", S, "net = Network{Hosts: append([]string{}, r.GetNetwork().GetHosts()...)}", "net = Network{Hosts: r.GetNetwork().GetHosts()}"},
	{"a pointer Refusal is a Refusal", S, "\t\terr = *p\n", "\t\t_ = p\n"},
	{"a Refusal's code is one a plugin refuses with", S, "case !refusable[refusal.Code]:", "case false:"},
	{"a Refusal has a warning", S, "case len(refusal.Warnings) == 0:", "case false:"},
	{"a Refusal within the envelope's limits", S, "why := checkStrings(refusal.Warnings, refusal.NotRead)", "why := \"\""},
	{"a plugin's error is internal", S, "if err != nil {\n\t\treturn fail(connect.CodeInternal, fmt.Sprintf(\"the plugin's %s failed", "if false {\n\t\treturn fail(connect.CodeInternal, fmt.Sprintf(\"the plugin's %s failed"},
	{"an Answer outside the envelope's limits is internal", S, "if why := checkStrings(answer.Warnings, answer.NotRead); why != \"\" {", "if why := \"\"; why != \"\" {"},
	{"data that is not JSON is internal", S, "data, err := plain(answer.Data)\n\tif err != nil {", "data, err := plain(answer.Data)\n\tif false && err != nil {"},
	{"describe names the runtime's package", S, "object[\"protocol\"], object[\"contracts\"] = Package, []any{Package}", "_ = object"},
	{"an answer over outputBytes is refused", S, "if len(out) > limit {", "if false {"},
	{"an answer of exactly outputBytes passes", S, "if len(out) > limit {", "if len(out) >= limit {"},
	{"only application/json is served", S, `mt != "application/json"`, `mt == "x"`},
	{"a compressed body is refused", S, `case r.Header.Get("Content-Encoding") != ""`, `case false && r.Header.Get("Content-Encoding") != ""`},
	{"a malformed Connect-Timeout-Ms is refused", S, "case header != \"\" && !timeoutMs.MatchString(header):", "case false:"},
	{"the listener is 127.0.0.1", S, `net.JoinHostPort("127.0.0.1"`, `net.JoinHostPort(""`},
	{"the stdio frame is strict JSON", S, "if err = strictCheck(line); err == nil {", "if err = nil; err == nil {"},
	{"the stdio frame has no unknown field", S, "\t\tdec.DisallowUnknownFields()\n", ""},
	{"the stdio frame's timeoutMs shortens the deadline", S, "time.Duration(ms)*time.Millisecond)\n", "noTimeout+0*time.Duration(ms))\n"},
	{"the stdio status line names the code", S, "status = code.String()", `status = "error"`},
	{"Serve with stdio returns the code it answered", S, `return connect.NewError(code, "the call answered "+code.String())`, "return nil"},
	{"a stdio call that fails exits 1", S, "os.Exit(1)", "os.Exit(0)"},
}

func main() {
	only := flag.String("run", "", "run only the guards whose description matches this regexp")
	flag.Parse()
	match := regexp.MustCompile(*only)
	root, err := os.Getwd()
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}
	var failed []string
	ran := 0
	for _, g := range guards {
		what, path, code, mutant := g[0], g[1], g[2], g[3]
		if !match.MatchString(what) {
			continue
		}
		ran++
		if why := run(root, path, code, mutant); why != "" {
			failed = append(failed, what+": "+why)
		} else {
			fmt.Println("ok  ", what)
		}
	}
	for _, f := range failed {
		fmt.Println("FAIL", f)
	}
	fmt.Printf("%d of %d guards are tested\n", ran-len(failed), ran)
	if len(failed) > 0 {
		os.Exit(1)
	}
}

// run applies one mutant to a copy of the module and says why the guard is not tested, or "".
func run(root, path, code, mutant string) string {
	tmp, err := os.MkdirTemp("", "guards-")
	if err != nil {
		return err.Error()
	}
	defer os.RemoveAll(tmp)
	for _, p := range []string{"go.mod", "go.sum", "runtime", "examples"} {
		if err := copyTree(filepath.Join(root, p), filepath.Join(tmp, p)); err != nil {
			return err.Error()
		}
	}
	target := filepath.Join(tmp, path)
	text, err := os.ReadFile(target)
	if err != nil {
		return err.Error()
	}
	if n := strings.Count(string(text), code); n != 1 {
		return fmt.Sprintf("the guard's code occurs %d times in %s", n, path)
	}
	if err := os.WriteFile(target, []byte(strings.Replace(string(text), code, mutant, 1)), 0o644); err != nil {
		return err.Error()
	}
	cmd := exec.Command("go", "test", "-count=1", "-failfast", "-parallel", "2", "./...")
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(), "GOFLAGS=-p=2")
	if out, err := cmd.CombinedOutput(); err == nil {
		return "the tests still pass without it"
	} else if strings.Contains(string(out), "[build failed]") || strings.Contains(string(out), "[setup failed]") {
		return "the mutant does not build:\n" + string(out)
	}
	return ""
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
}
