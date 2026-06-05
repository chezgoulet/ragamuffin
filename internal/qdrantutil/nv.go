// Package qdrantutil provides shared helpers for working with the Qdrant Go SDK.
//
// The main entry point is Nv — a panic-on-error wrapper around qdrant.NewValue.
// Duplicated across server and pruner before extraction, now lives here.
package qdrantutil

import "github.com/qdrant/go-client/qdrant"

// Nv wraps qdrant.NewValue, panicking on error.
// All call sites pass primitive types (string, bool, float64) that cannot
// produce NewValue errors at runtime. Go's type system forces error capture.
func Nv(v any) *qdrant.Value {
	r, err := qdrant.NewValue(v)
	if err != nil {
		panic("NewValue: " + err.Error())
	}
	return r
}

// NvList creates a Qdrant list value from a string slice.
func NvList(items []string) *qdrant.Value {
	values := make([]*qdrant.Value, len(items))
	for i, s := range items {
		values[i] = Nv(s)
	}
	return &qdrant.Value{
		Kind: &qdrant.Value_ListValue{
			ListValue: &qdrant.ListValue{Values: values},
		},
	}
}
