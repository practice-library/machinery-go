// Package runtime serves a plugin as machinery.plugin.v1alpha1.PluginService, contract v1alpha1 of
// practice-library/machinery: Connect unary JSON on 127.0.0.1, or one call over stdin and stdout.
//
// A plugin is any value with some of the methods below; the runtime answers unimplemented for the rest.
// Per call, the runtime and not the plugin decodes strictly and checks the rules proto cannot state
// (invalid_argument), gives up at the deadline (deadline_exceeded, whether or not the plugin honours ctx),
// writes every envelope field with integral numbers as integers, refuses an answer over caps.outputBytes
// (resource_exhausted), and turns a Refusal into its code and anything else that goes wrong into internal.
// Every one of those errors carries its Failure detail and fits caps.outputBytes.
package runtime

import (
	"context"
	"strings"

	"connectrpc.com/connect/v2"
)

// Describer answers Describe. Data is an object with name, version, job and verbs; the runtime sets
// protocol and contracts to the package it serves.
type Describer interface {
	Describe(ctx context.Context, caps Caps) (Answer, error)
}

// Planner answers Plan.
type Planner interface {
	Plan(ctx context.Context, input map[string]any, caps Caps) (Answer, error)
}

// UnitRunner answers RunUnit.
type UnitRunner interface {
	RunUnit(ctx context.Context, input map[string]any, net Network, caps Caps) (Answer, error)
}

// Merger answers Merge.
type Merger interface {
	Merge(ctx context.Context, input map[string]any, caps Caps) (Answer, error)
}

// Runner answers Run.
type Runner interface {
	Run(ctx context.Context, input map[string]any, net Network, caps Caps) (Answer, error)
}

// Caps are the caller's limits, already checked: TimeSeconds 1 to 3600, OutputBytes 1024 to 16777216.
type Caps struct {
	TimeSeconds int
	OutputBytes int
}

// Network is the request's network with its defaults filled in: Hosts is never nil, and with Replay it
// is empty.
type Network struct {
	Hosts  []string
	Replay *Replay
}

// Replay stands in for the network. Exchanges maps a URL to its recorded responses, each an object as
// the contract states it (status, headers, body, or error alone); it is never nil.
type Replay struct {
	Start     string
	Exchanges map[string][]any
}

// NotRead is an input the plugin was given and deliberately did not use.
type NotRead struct {
	What string
	Why  string
}

// Answer is what a call answers when it succeeds. Data is any JSON value: nil, bool, string, a number,
// []any, map[string]any, or anything encoding/json writes. Warnings are 1 to 500 characters, and
// NotRead's What and Why 1 to 300 (envelope.schema.json); an Answer that breaks that is internal.
//
// Numbers are the contract's doubles. In input, an integral value with |v| <= 2^53 arrives as int64
// and every other number as float64; on the way out, an int64 beyond 2^53 becomes the nearest double, and
// every integral number is written in integer form (3, never 3.0; 1e20 as 100000000000000000000).
type Answer struct {
	Data     any
	NotRead  []NotRead
	Warnings []string
}

// Refusal is returned as the error of a call that fails on purpose. Code is one of invalid_argument,
// failed_precondition, resource_exhausted, unimplemented and unavailable, with at least one warning
// saying why, within the envelope's limits; a Refusal that breaks that is internal.
type Refusal struct {
	Code     connect.Code
	Warnings []string
	NotRead  []NotRead
}

func (r Refusal) Error() string { return r.Code.String() + ": " + strings.Join(r.Warnings, "; ") }

// refusable are the codes a plugin may refuse with; deadline_exceeded and internal are the runtime's.
var refusable = map[connect.Code]bool{
	connect.CodeInvalidArgument: true, connect.CodeFailedPrecondition: true,
	connect.CodeResourceExhausted: true, connect.CodeUnimplemented: true, connect.CodeUnavailable: true,
}
