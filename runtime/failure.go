package runtime

// Every error the runtime answers: a Connect error whose message is its warnings joined by "; " and
// which carries the envelope (ok false, data null) as one machinery.plugin.v1alpha1.Failure detail, as
// plugin.proto requires of every error the plugin returns.
//
// The error body fits caps.outputBytes by saying less, never by dropping the detail: notRead goes first,
// then every warning but the first, then the end of that warning. Only below the smallest outputBytes the
// contract allows (1024), where not even one character fits, are the detail and the message dropped.

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"
	pb "github.com/practice-library/machinery/gen/go/machinery/plugin/v1alpha1"
	"google.golang.org/protobuf/types/known/structpb"
)

const warningMax, notReadMax = 500, 300 // envelope.schema.json, in characters

// checkStrings says why warnings or notRead break envelope.schema.json's limits, or "".
func checkStrings(warnings []string, notRead []NotRead) string {
	for _, w := range warnings {
		if n := utf8.RuneCountInString(w); n < 1 || n > warningMax {
			return fmt.Sprintf("a warning is 1 to %d characters, not %q", warningMax, cut(w, 40))
		}
	}
	for _, nr := range notRead {
		for _, s := range []string{nr.What, nr.Why} {
			if n := utf8.RuneCountInString(s); n < 1 || n > notReadMax {
				return fmt.Sprintf("a notRead what and why are 1 to %d characters, not %q", notReadMax, cut(s, 40))
			}
		}
	}
	return ""
}

// parts is the error for code saying warnings and notRead, with its Failure detail.
func parts(code connect.Code, warnings []string, notRead []NotRead) *connect.Error {
	f := &pb.Failure{Data: structpb.NewNullValue(), Warnings: warnings}
	for _, nr := range notRead {
		f.NotRead = append(f.NotRead, &pb.NotRead{What: nr.What, Why: nr.Why})
	}
	detail, err := connectproto.NewErrorDetail(f)
	if err != nil { // strings that are not UTF-8: say so instead
		return parts(code, []string{"the error's warnings are not UTF-8"}, nil)
	}
	return connect.NewError(code, strings.Join(warnings, "; ")).WithDetail(detail)
}

// failure is the error for code, with its Failure detail, fitted into limit bytes (none when limit < 1).
func failure(code connect.Code, warnings []string, notRead []NotRead, limit int) *connect.Error {
	cutWarnings := make([]string, len(warnings))
	for i, w := range warnings {
		cutWarnings[i] = cut(w, warningMax)
	}
	warnings = cutWarnings
	e := parts(code, warnings, notRead)
	if limit > 0 && len(errorBody(e)) > limit {
		e = parts(code, warnings, nil)
	}
	if limit > 0 && len(errorBody(e)) > limit && len(warnings) == 0 {
		return connect.NewError(code, "")
	}
	if limit > 0 && len(errorBody(e)) > limit {
		first := []rune(warnings[0])
		lo, hi := 0, len(first) // the longest prefix of the first warning alone that fits, by bisection
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if len(errorBody(parts(code, []string{string(first[:mid])}, nil))) <= limit {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		e = connect.NewError(code, "")
		if lo > 0 {
			e = parts(code, []string{string(first[:lo])}, nil)
		}
	}
	return e
}

// errorBody is the Connect JSON error body of e: code, message and each detail's type and unpadded
// base64 value. The runtime writes these bytes itself on both bindings and never through connect-go's
// error writer, which would add a debug copy of each detail and HTML-escape the text (connect/v2 v2.0.2
// connecthttp/protocol_connect.go), so this size is what is sent.
func errorBody(e *connect.Error) []byte {
	b := append(appendString([]byte(`{"code":`), e.Code().String()), `,"message":`...)
	b = appendString(b, e.Message())
	if details := e.Details(); len(details) > 0 {
		b = append(b, `,"details":[`...)
		for i, d := range details {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(appendString(append(b, `{"type":`...), d.Type), `,"value":`...)
			b = append(appendString(b, base64.RawStdEncoding.EncodeToString(d.Value)), '}')
		}
		b = append(b, ']')
	}
	return append(b, '}')
}
