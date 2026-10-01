package bendssz

// Unmarshaler is what the generated SSZ types implement.
type Unmarshaler interface {
	UnmarshalSSZ([]byte) error
}

// UnmarshalSSZ is the drop-in used by Prysm's decode paths: with the bendssz
// build tag and a registered type the bytes go through the C library (the
// Bend-checked decoder) and the Go struct is built from the C object;
// otherwise, and whenever the C decoder rejects the bytes, the type's own
// generated UnmarshalSSZ decides (and produces the error).
func UnmarshalSSZ(msg Unmarshaler, b []byte) error {
	if handled, err := Unmarshal(msg, b); handled {
		return err
	}
	return msg.UnmarshalSSZ(b)
}
