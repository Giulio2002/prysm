// Package bendssz decodes SSZ through the formally checked bend-ssz library
// (SSZ written and proved in Bend, compiled to C) instead of the generated
// fastssz-style UnmarshalSSZ methods.
//
// The flow for one object, with the build tag `bendssz`:
//
//	SSZ bytes --cgo--> libbendssz (C): the Bend runtime validates the bytes,
//	                   decodes them into its own heap object and flattens that
//	                   object into a plain C byte stream (the "C object")
//	C stream  --Go---> the Go (protobuf) struct, built field by field by a plan
//	                   compiled once per type from the struct tags (plan.go)
//
// Only the types (the Go structs Prysm uses) are built on the Go side; all
// parsing, validation and limit checks happen in C. HashTreeRoot and
// serialization stay on the fastssz side.
//
// Without the tag this package is a stub: Unmarshal reports "not handled" and
// every caller falls back to its fastssz UnmarshalSSZ.
package bendssz
