package runtime

// The two front ends, 127.0.0.1 HTTP (the reference binding) and stdio, over one core that takes a path
// and a body and answers a code and a body.
//
// The five paths are served with net/http, not connect-go's handler: connect-go wraps a codec's error in
// its own (so a decoding error would lose its Failure detail), writes a "debug" copy of every detail it can
// resolve (so an error's size is not what the runtime fitted to outputBytes), and is not a bytes-in,
// bytes-out core that stdio can share. connect-go still gives the codes, the error and the detail.
//
// What the HTTP front end answers before the core runs has no Failure detail, as a transport's answer: 404
// for a path not served (Connect clients read it as unimplemented), 405 for a method other than POST, 415
// for a body that is not application/json.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"

	"connectrpc.com/connect/v2"
	pb "github.com/practice-library/machinery/gen/go/machinery/plugin/v1alpha1"
	"github.com/practice-library/machinery/gen/go/machinery/plugin/v1alpha1/pluginv1alpha1connect"
	"google.golang.org/protobuf/proto"
)

// Package is the protobuf package served; Describe answers it as protocol and contracts.
const Package = "machinery.plugin.v1alpha1"

const maxRequestBytes = 16 << 20

type callSpec struct {
	name    string
	request func() proto.Message
}

var calls = map[string]callSpec{
	pluginv1alpha1connect.PluginServiceDescribeProcedure: {"Describe", func() proto.Message { return new(pb.DescribeRequest) }},
	pluginv1alpha1connect.PluginServicePlanProcedure:     {"Plan", func() proto.Message { return new(pb.PlanRequest) }},
	pluginv1alpha1connect.PluginServiceRunUnitProcedure:  {"RunUnit", func() proto.Message { return new(pb.RunUnitRequest) }},
	pluginv1alpha1connect.PluginServiceMergeProcedure:    {"Merge", func() proto.Message { return new(pb.MergeRequest) }},
	pluginv1alpha1connect.PluginServiceRunProcedure:      {"Run", func() proto.Message { return new(pb.RunRequest) }},
}

type method func(ctx context.Context, input map[string]any, net Network, caps Caps) (Answer, error)

// methodOf is plugin's method for call, or nil.
func methodOf(plugin any, call string) method {
	switch call {
	case "Describe":
		if p, ok := plugin.(Describer); ok {
			return func(ctx context.Context, _ map[string]any, _ Network, c Caps) (Answer, error) {
				return p.Describe(ctx, c)
			}
		}
	case "Plan":
		if p, ok := plugin.(Planner); ok {
			return func(ctx context.Context, in map[string]any, _ Network, c Caps) (Answer, error) {
				return p.Plan(ctx, in, c)
			}
		}
	case "Merge":
		if p, ok := plugin.(Merger); ok {
			return func(ctx context.Context, in map[string]any, _ Network, c Caps) (Answer, error) {
				return p.Merge(ctx, in, c)
			}
		}
	case "RunUnit":
		if p, ok := plugin.(UnitRunner); ok {
			return p.RunUnit
		}
	case "Run":
		if p, ok := plugin.(Runner); ok {
			return p.Run
		}
	}
	return nil
}

type core struct{ plugin any }

// handle answers one call: code 0 and the envelope on success, or a code and its Connect error body.
// served is false for a path that is not one of the five calls.
func (s core) handle(ctx context.Context, path string, body []byte, timeout time.Duration) (code connect.Code, out []byte, served bool) {
	spec, ok := calls[path]
	if !ok {
		return connect.CodeUnimplemented, errorBody(connect.NewError(connect.CodeUnimplemented, "the path "+strconv.Quote(cut(path, 120))+" is not served")), false
	}
	fail := func(code connect.Code, why string, notRead []NotRead, limit int, more ...string) (connect.Code, []byte, bool) {
		return code, errorBody(failure(code, append([]string{why}, more...), notRead, limit)), true
	}
	req := spec.request()
	if err := decode(body, req); err != nil {
		// outputBytes is inside the body that did not decode, so the error fits the smallest one allowed
		return fail(connect.CodeInvalidArgument, fmt.Sprintf("the body is not a valid %s: %v", req.ProtoReflect().Descriptor().Name(), err), nil, int(outputBytes[0]))
	}
	pcaps := req.(withCaps).GetCaps()
	limit := int(pcaps.GetOutputBytes())
	if why := check(spec.name, req); why != "" {
		return fail(connect.CodeInvalidArgument, why, nil, limit)
	}
	fn := methodOf(s.plugin, spec.name)
	if fn == nil {
		return fail(connect.CodeUnimplemented, "this plugin does not answer "+spec.name, nil, limit)
	}
	caps := Caps{TimeSeconds: int(pcaps.GetTimeSeconds()), OutputBytes: limit}
	var input map[string]any
	if r, ok := req.(withInput); ok {
		input = fromStruct(r.GetInput())
	}
	var net Network
	if r, ok := req.(withNetwork); ok {
		net = Network{Hosts: append([]string{}, r.GetNetwork().GetHosts()...)}
		if replay := r.GetNetwork().GetReplay(); replay != nil {
			net.Replay = &Replay{Start: replay.GetStart(), Exchanges: map[string][]any{}}
			for url, list := range replay.GetExchanges() {
				net.Replay.Exchanges[url] = fromList(list)
			}
		}
	}
	d := deadline(caps, timeout)
	answer, err, late := within(ctx, d, func(ctx context.Context) (Answer, error) { return fn(ctx, input, net, caps) })
	if late {
		return fail(connect.CodeDeadlineExceeded, fmt.Sprintf("the call did not finish within %g seconds", d.Seconds()), nil, limit)
	}
	if p, ok := errors.AsType[*Refusal](err); ok && p != nil {
		err = *p
	}
	if refusal, ok := errors.AsType[Refusal](err); ok {
		why := checkStrings(refusal.Warnings, refusal.NotRead)
		switch {
		case !refusable[refusal.Code]:
			why = "its code is " + refusal.Code.String() + ", which a plugin does not refuse with"
		case len(refusal.Warnings) == 0:
			why = "it has no warning"
		case why == "":
			return fail(refusal.Code, refusal.Warnings[0], refusal.NotRead, limit, refusal.Warnings[1:]...)
		}
		return fail(connect.CodeInternal, "the plugin's "+spec.name+" refusal: "+why, nil, limit)
	}
	if err != nil {
		return fail(connect.CodeInternal, fmt.Sprintf("the plugin's %s failed: %T", spec.name, err), nil, limit)
	}
	if why := checkStrings(answer.Warnings, answer.NotRead); why != "" {
		return fail(connect.CodeInternal, "the plugin's "+spec.name+" answer: "+why, nil, limit)
	}
	data, err := plain(answer.Data)
	if err != nil {
		return fail(connect.CodeInternal, "the plugin's "+spec.name+" answer is not JSON: "+cut(err.Error(), 120), nil, limit)
	}
	if spec.name == "Describe" {
		object, ok := data.(map[string]any)
		if !ok {
			return fail(connect.CodeInternal, "the plugin's describe data is not an object", nil, limit)
		}
		object["protocol"], object["contracts"] = Package, []any{Package}
	}
	out = envelope(data, answer.NotRead, answer.Warnings)
	if len(out) > limit {
		return fail(connect.CodeResourceExhausted, fmt.Sprintf("the answer is larger than outputBytes (%d > %d bytes)", len(out), limit), nil, limit)
	}
	return 0, out, true
}

// httpStatus is the HTTP status Connect gives each code the runtime answers.
var httpStatus = map[connect.Code]int{
	0: 200, connect.CodeInvalidArgument: 400, connect.CodeFailedPrecondition: 400,
	connect.CodeResourceExhausted: 429, connect.CodeUnimplemented: 501, connect.CodeUnavailable: 503,
	connect.CodeDeadlineExceeded: 504, connect.CodeInternal: 500,
}

var timeoutMs = regexp.MustCompile(`^[0-9]{1,10}$`) // Connect's Connect-Timeout-Ms

// Handler serves plugin's five calls as Connect unary JSON, uncompressed.
func Handler(plugin any) http.Handler {
	s := core{plugin}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := calls[r.URL.Path]; !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			http.Error(w, "application/json only", http.StatusUnsupportedMediaType)
			return
		}
		smallest := int(outputBytes[0])
		code, out := connect.Code(0), []byte(nil)
		timeout := noTimeout
		body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
		header := r.Header.Get("Connect-Timeout-Ms")
		switch {
		case r.Header.Get("Content-Encoding") != "" && r.Header.Get("Content-Encoding") != "identity":
			code, out = connect.CodeUnimplemented, errorBody(failure(connect.CodeUnimplemented, []string{"a request body is not compressed"}, nil, smallest))
		case header != "" && !timeoutMs.MatchString(header):
			code, out = connect.CodeInvalidArgument, errorBody(failure(connect.CodeInvalidArgument, []string{"Connect-Timeout-Ms is 1 to 10 digits"}, nil, smallest))
		case err != nil || len(body) > maxRequestBytes:
			code, out = connect.CodeInvalidArgument, errorBody(failure(connect.CodeInvalidArgument, []string{"the body could not be read whole, or is over 16 MiB"}, nil, smallest))
		default:
			if header != "" {
				ms, _ := strconv.ParseInt(header, 10, 64)
				timeout = time.Duration(ms) * time.Millisecond
			}
			code, out, _ = s.handle(r.Context(), r.URL.Path, body, timeout)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(out)))
		w.WriteHeader(httpStatus[code])
		_, _ = w.Write(out)
	})
}

// stdio answers one call framed as machinery's stdio binding: on in, one JSON line {"path": ...,
// "timeoutMs": N} and then the request body; on out, one line {"status": <code or "ok">} and then the
// response or Connect error body. It returns the code, 0 on ok.
func stdio(ctx context.Context, plugin any, in io.Reader, out io.Writer) connect.Code {
	r := bufio.NewReader(io.LimitReader(in, maxRequestBytes+64<<10))
	line, err := r.ReadBytes('\n')
	var frame struct {
		Path      *string      `json:"path"`
		TimeoutMs *json.Number `json:"timeoutMs"`
	}
	code, body := connect.Code(0), []byte(nil)
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		dec.UseNumber()
		if err = strictCheck(line); err == nil && dec.Decode(&frame) != nil {
			err = errFrame // encoding/json's own text can quote the frame
		}
	}
	if err == nil && (frame.Path == nil || frame.TimeoutMs == nil || !timeoutMs.MatchString(frame.TimeoutMs.String())) {
		err = errFrame
	}
	request, rerr := io.ReadAll(r)
	switch {
	case err != nil:
		code = connect.CodeInvalidArgument
		body = errorBody(failure(code, []string{"the frame's first line is not valid: " + err.Error()}, nil, int(outputBytes[0])))
	case rerr != nil || len(request) > maxRequestBytes:
		code = connect.CodeInvalidArgument
		body = errorBody(failure(code, []string{"the body could not be read whole, or is over 16 MiB"}, nil, int(outputBytes[0])))
	default:
		ms, _ := strconv.ParseInt(frame.TimeoutMs.String(), 10, 64)
		code, body, _ = core{plugin}.handle(ctx, *frame.Path, request, time.Duration(ms)*time.Millisecond)
	}
	status := "ok"
	if code != 0 {
		status = code.String()
	}
	fmt.Fprintf(out, "{\"status\":%q}\n", status)
	_, _ = out.Write(body)
	return code
}

// Options say how Serve serves.
type Options struct {
	Stdio  bool      // one call on Stdin and Stdout, instead of a listener on 127.0.0.1
	Port   int       // the 127.0.0.1 port; 0 picks a free one
	Stdin  io.Reader // default os.Stdin
	Stdout io.Writer // default os.Stdout
}

// Serve serves plugin. On 127.0.0.1 it prints the base URL (http://127.0.0.1:<port>) as the first line
// of Stdout and serves until the listener fails. With Stdio it answers one call and returns nil when the
// call succeeded, or a *connect.Error with the code it answered.
func Serve(plugin any, opts Options) error {
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stdio {
		w := bufio.NewWriter(opts.Stdout)
		code := stdio(context.Background(), plugin, opts.Stdin, w)
		if err := w.Flush(); err != nil {
			return err
		}
		if code != 0 {
			return connect.NewError(code, "the call answered "+code.String())
		}
		return nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(opts.Port)))
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(opts.Stdout, "http://%s\n", ln.Addr()); err != nil {
		return err
	}
	server := &http.Server{Handler: Handler(plugin), ReadHeaderTimeout: 10 * time.Second}
	return server.Serve(ln)
}

// Main serves plugin as a command: -stdio for one call on stdin and stdout, otherwise 127.0.0.1 on -port.
// It exits 0 when a stdio call succeeds and 1 on any error.
func Main(plugin any) {
	var opts Options
	flag.BoolVar(&opts.Stdio, "stdio", false, "answer one call on stdin and stdout")
	flag.IntVar(&opts.Port, "port", 0, "the 127.0.0.1 port to serve on (0: any free one)")
	flag.Parse()
	if err := Serve(plugin, opts); err != nil {
		var ce *connect.Error
		if !errors.As(err, &ce) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

// errFrame is the one reason a stdio frame line is refused that is not the pre-decode's own.
var errFrame = errors.New(`it is {"path": <string>, "timeoutMs": <0 to 9999999999>}`)
