package runtime

// The codec: contract v1alpha1's wire rules on top of proto's JSON mapping.
//
// Decoding is strict. The body must be UTF-8 JSON, one value, with no key given twice at any depth, no NaN
// or Infinity, no lone surrogate, and every number a finite double; then protojson decodes it with unknown
// fields refused. Proto's own leniency stays, as the contract allows and machinery-python keeps it: proto
// field names (time_seconds), integers written as strings, and null for a message field, read as left out.
// (protojson v1.36.12 alone refuses each raw-body fixture too; the pre-decode keeps the contract's rules
// from depending on one library's choices.)
//
// Encoding writes every envelope field (data is null when the call gives none), and every number with an
// integral value as an integer: 3, never 3.0, and 1e20 as 100000000000000000000 (plugin.proto's header).
// Non-integral numbers are written as Python's repr writes them, so both runtimes write the same bytes for
// every number (machinery-python's matching change for integral values above 2^53 is on its branch
// integral-form-any-size; its main still writes 9007199254740994.0 for 2^53 + 2).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const maxExact = 1 << 53 // every integer up to it is exact as a double

// strictCheck says why body is not strict JSON, or nil.
func strictCheck(body []byte) error {
	if !utf8.Valid(body) {
		return errors.New("the body is not UTF-8")
	}
	if err := surrogates(body); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := strictValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("the body holds more than one JSON value")
	}
	return nil
}

func strictValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err == io.EOF {
		return errors.New("the body is empty")
	} else if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		seen := map[string]bool{}
		for dec.More() {
			if t == '{' {
				key, _ := dec.Token() // a string: the decoder refuses anything else as a key
				k, _ := key.(string)
				if seen[k] {
					return fmt.Errorf("the key %q is given twice", k)
				}
				seen[k] = true
			}
			if err := strictValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token() // the closing delimiter
		return err
	case json.Number:
		if _, err := strconv.ParseFloat(string(t), 64); err != nil {
			return fmt.Errorf("the number %.40s is not a finite double", t)
		}
	}
	return nil
}

// surrogates refuses a \u escape of a surrogate that is not half of a pair. In JSON a backslash occurs
// only inside a string, so every backslash starts an escape.
func surrogates(b []byte) error {
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		i++
		r := hex4(b, i)
		if r < 0xD800 || r > 0xDFFF {
			continue
		}
		if r <= 0xDBFF && hex4(b, i+6) >= 0xDC00 && hex4(b, i+6) <= 0xDFFF && b[i+5] == '\\' {
			i += 10
			continue
		}
		return errors.New("a string holds a lone surrogate")
	}
	return nil
}

// hex4 is the code unit of the \u escape whose u is at b[i], or -1.
func hex4(b []byte, i int) int {
	if i+4 >= len(b) || b[i] != 'u' {
		return -1
	}
	n, err := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
	if err != nil {
		return -1
	}
	return int(n)
}

// decode fills msg from body, or says why the body is not one.
func decode(body []byte, msg proto.Message) error {
	if err := strictCheck(body); err != nil {
		return err
	}
	return protojson.UnmarshalOptions{DiscardUnknown: false}.Unmarshal(body, msg)
}

// fromValue is v as a plain Go value, every integral number up to 2^53 an int64.
func fromValue(v *structpb.Value) any {
	switch k := v.GetKind().(type) {
	case *structpb.Value_NumberValue:
		return number(k.NumberValue)
	case *structpb.Value_StringValue:
		return k.StringValue
	case *structpb.Value_BoolValue:
		return k.BoolValue
	case *structpb.Value_StructValue:
		return fromStruct(k.StructValue)
	case *structpb.Value_ListValue:
		return fromList(k.ListValue)
	}
	return nil
}

func fromStruct(s *structpb.Struct) map[string]any {
	out := make(map[string]any, len(s.GetFields()))
	for k, v := range s.GetFields() {
		out[k] = fromValue(v)
	}
	return out
}

func fromList(l *structpb.ListValue) []any {
	out := make([]any, 0, len(l.GetValues()))
	for _, v := range l.GetValues() {
		out = append(out, fromValue(v))
	}
	return out
}

// jsonNumber is RFC 8259's number grammar, nothing more.
var jsonNumber = regexp.MustCompile(`\A-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?\z`)

func number(f float64) any {
	if f == math.Trunc(f) && math.Abs(f) <= maxExact {
		return int64(f)
	}
	return f
}

// plain is v as JSON values (nil, bool, string, float64, []any, map[string]any), or why it is not one.
func plain(v any) (any, error) {
	switch x := v.(type) {
	case nil, bool, string:
		return x, nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, errors.New("a number is not finite")
		}
		return x, nil
	case json.Number: // from a plugin, not from the decoder: ParseFloat also reads NaN, Inf and hex
		if !jsonNumber.MatchString(string(x)) {
			return nil, errors.New("a json.Number is not a JSON number")
		}
		f, err := strconv.ParseFloat(string(x), 64) // an overflow is ErrRange, so a finite f is all that passes
		if err != nil {
			return nil, errors.New("a number is not finite")
		}
		return f, nil
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			var err error
			if out[i], err = plain(item); err != nil {
				return nil, err
			}
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			var err error
			if out[k], err = plain(item); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	raw, err := json.Marshal(v) // other numbers, typed slices and maps, structs: an int64 becomes the nearest double
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var back any
	if err := dec.Decode(&back); err != nil {
		return nil, err
	}
	return plain(back)
}

// appendJSON writes a plain value compactly: keys sorted, integral numbers as integers.
func appendJSON(b []byte, v any) []byte {
	switch x := v.(type) {
	case bool:
		return strconv.AppendBool(b, x)
	case string:
		return appendString(b, x)
	case float64:
		if x == 0 {
			return append(b, '0') // -0 too
		}
		if x == math.Trunc(x) { // every integral double as integer digits: 1e20 is 100000000000000000000
			return strconv.AppendFloat(b, x, 'f', -1, 64)
		}
		// Non-integral: shortest round-trip digits, positional while 1e-4 <= |x| < 1e16 and with an exponent
		// outside, as Python's repr writes them, so both runtimes write the same bytes (1234567.5, 1e-05).
		// (every double from 2^53 up is integral, so the upper bound is never reached here; it states repr's rule)
		if a := math.Abs(x); a >= 1e-4 && a < 1e16 {
			return strconv.AppendFloat(b, x, 'f', -1, 64)
		}
		return strconv.AppendFloat(b, x, 'e', -1, 64)
	case []any:
		b = append(b, '[')
		for i, item := range x {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendJSON(b, item)
		}
		return append(b, ']')
	case map[string]any:
		b = append(b, '{')
		for i, k := range slices.Sorted(maps.Keys(x)) {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(appendString(b, k), ':')
			b = appendJSON(b, x[k])
		}
		return append(b, '}')
	}
	return append(b, "null"...)
}

// appendString writes s as a JSON string, without encoding/json's HTML escapes.
func appendString(b []byte, s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return append(b, bytes.TrimSuffix(buf.Bytes(), []byte("\n"))...)
}

// envelope is a success body: every envelope field, data as given (a plain value).
func envelope(data any, notRead []NotRead, warnings []string) []byte {
	b := append([]byte(`{"ok":true,"data":`), appendJSON(nil, data)...)
	b = append(b, `,"notRead":`...)
	b = appendNotRead(b, notRead)
	b = append(b, `,"warnings":`...)
	return append(appendStrings(b, warnings), '}')
}

func appendNotRead(b []byte, notRead []NotRead) []byte {
	b = append(b, '[')
	for i, n := range notRead {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(appendString(append(b, `{"what":`...), n.What), `,"why":`...)
		b = append(appendString(b, n.Why), '}')
	}
	return append(b, ']')
}

func appendStrings(b []byte, ss []string) []byte {
	b = append(b, '[')
	for i, s := range ss {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendString(b, s)
	}
	return append(b, ']')
}
