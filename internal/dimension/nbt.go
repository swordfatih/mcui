package dimension

// A small little-endian NBT reader/writer for the Bedrock records used by the
// dimension importer. It keeps tag types and list element types intact.
import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

type tag struct {
	kind  byte
	value any
}
type compound map[string]tag
type list struct {
	kind   byte
	values []tag
}

func readNBT(data []byte) (tag, error) {
	root, used, err := readNBTPrefix(data)
	if err != nil {
		return tag{}, err
	}
	if used != len(data) {
		return tag{}, errors.New("trailing NBT data")
	}
	return root, nil
}

func readNBTPrefix(data []byte) (tag, int, error) {
	r := bytes.NewReader(data)
	k, err := r.ReadByte()
	if err != nil || k != 10 {
		return tag{}, 0, errors.New("expected compound NBT")
	}
	if _, err = readString(r); err != nil {
		return tag{}, 0, err
	}
	v, err := readPayload(r, k, 0)
	if err != nil {
		return tag{}, 0, err
	}
	return tag{k, v}, len(data) - r.Len(), nil
}

func readString(r *bytes.Reader) (string, error) {
	var n uint16
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return "", err
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return string(b), err
}
func readPayload(r *bytes.Reader, kind byte, depth int) (any, error) {
	if depth > 128 {
		return nil, errors.New("NBT nesting is too deep")
	}
	switch kind {
	case 1:
		v, e := r.ReadByte()
		return int8(v), e
	case 2:
		var v int16
		e := binary.Read(r, binary.LittleEndian, &v)
		return v, e
	case 3:
		var v int32
		e := binary.Read(r, binary.LittleEndian, &v)
		return v, e
	case 4:
		var v int64
		e := binary.Read(r, binary.LittleEndian, &v)
		return v, e
	case 5:
		var v float32
		e := binary.Read(r, binary.LittleEndian, &v)
		return v, e
	case 6:
		var v float64
		e := binary.Read(r, binary.LittleEndian, &v)
		return v, e
	case 7:
		var n int32
		if e := binary.Read(r, binary.LittleEndian, &n); e != nil {
			return nil, e
		}
		if n < 0 || n > 64<<20 {
			return nil, errors.New("invalid NBT byte array length")
		}
		b := make([]byte, n)
		_, e := io.ReadFull(r, b)
		return b, e
	case 8:
		return readString(r)
	case 9:
		k, e := r.ReadByte()
		if e != nil {
			return nil, e
		}
		var n int32
		if e = binary.Read(r, binary.LittleEndian, &n); e != nil {
			return nil, e
		}
		if n < 0 || n > 1<<22 {
			return nil, errors.New("invalid NBT list length")
		}
		values := make([]tag, n)
		for i := range values {
			v, err := readPayload(r, k, depth+1)
			if err != nil {
				return nil, err
			}
			values[i] = tag{k, v}
		}
		return list{k, values}, nil
	case 10:
		m := compound{}
		for {
			k, e := r.ReadByte()
			if e != nil {
				return nil, e
			}
			if k == 0 {
				break
			}
			name, e := readString(r)
			if e != nil {
				return nil, e
			}
			v, e := readPayload(r, k, depth+1)
			if e != nil {
				return nil, e
			}
			if _, ok := m[name]; ok {
				return nil, fmt.Errorf("duplicate NBT field %q", name)
			}
			m[name] = tag{k, v}
		}
		return m, nil
	case 11, 12:
		var n int32
		if e := binary.Read(r, binary.LittleEndian, &n); e != nil {
			return nil, e
		}
		if n < 0 || n > 1<<22 {
			return nil, errors.New("invalid NBT array length")
		}
		vals := make([]tag, n)
		child := byte(3)
		if kind == 12 {
			child = 4
		}
		for i := range vals {
			v, e := readPayload(r, child, depth+1)
			if e != nil {
				return nil, e
			}
			vals[i] = tag{child, v}
		}
		return vals, nil
	default:
		return nil, fmt.Errorf("unsupported NBT tag %d", kind)
	}
}

func writeNBT(root tag) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte(root.kind)
	_ = binary.Write(&b, binary.LittleEndian, uint16(0))
	if err := writePayload(&b, root, 0); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
func writeString(w *bytes.Buffer, s string) error {
	if len(s) > 65535 {
		return errors.New("NBT string too long")
	}
	_ = binary.Write(w, binary.LittleEndian, uint16(len(s)))
	w.WriteString(s)
	return nil
}
func writePayload(w *bytes.Buffer, t tag, depth int) error {
	if depth > 128 {
		return errors.New("NBT nesting is too deep")
	}
	var err error
	switch t.kind {
	case 1:
		return w.WriteByte(byte(t.value.(int8)))
	case 2, 3, 4, 5, 6:
		return binary.Write(w, binary.LittleEndian, t.value)
	case 7:
		v := t.value.([]byte)
		_ = binary.Write(w, binary.LittleEndian, int32(len(v)))
		_, err = w.Write(v)
		return err
	case 8:
		return writeString(w, t.value.(string))
	case 9:
		v := t.value.(list)
		if err = w.WriteByte(v.kind); err != nil {
			return err
		}
		if len(v.values) > 1<<22 {
			return errors.New("NBT list too long")
		}
		_ = binary.Write(w, binary.LittleEndian, int32(len(v.values)))
		for _, x := range v.values {
			if x.kind != v.kind {
				return errors.New("mixed NBT list")
			}
			if err = writePayload(w, x, depth+1); err != nil {
				return err
			}
		}
		return nil
	case 10:
		v := t.value.(compound)
		for name, x := range v {
			if x.kind == 0 {
				return errors.New("invalid NBT child")
			}
			if err = w.WriteByte(x.kind); err != nil {
				return err
			}
			if err = writeString(w, name); err != nil {
				return err
			}
			if err = writePayload(w, x, depth+1); err != nil {
				return err
			}
		}
		return w.WriteByte(0)
	case 11, 12:
		v := t.value.([]tag)
		_ = binary.Write(w, binary.LittleEndian, int32(len(v)))
		for _, x := range v {
			if err = writePayload(w, x, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported NBT tag %d", t.kind)
	}
}

func intTag(v int32) tag                { return tag{3, v} }
func strTag(v string) tag               { return tag{8, v} }
func asCompound(t tag) (compound, bool) { v, ok := t.value.(compound); return v, ok }
func intValue(t tag) (int32, bool)      { v, ok := t.value.(int32); return v, ok }
