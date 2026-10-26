package main

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf16"
)

const androidNamespace = "http://schemas.android.com/apk/res/android"

// decodeManifest preserves the actual packaged manifest, including Fyne's
// synthesized SDK and icon attributes. It rejects unsupported typed values
// rather than replacing the manifest with a second approximation of it.
func decodeManifest(data []byte) ([]xml.Token, error) {
	invalid := errors.New("unsupported compiled widget merge manifest")
	if len(data) < 8 || binary.LittleEndian.Uint16(data) != 3 || binary.LittleEndian.Uint16(data[2:]) != 8 || int(binary.LittleEndian.Uint32(data[4:])) != len(data) {
		return nil, invalid
	}
	var pool []string
	var tokens []xml.Token
	var stack []xml.Name
	str := func(index uint32) (string, error) {
		if uint64(index) >= uint64(len(pool)) {
			return "", invalid
		}
		return pool[index], nil
	}
	name := func(ns, id uint32) (xml.Name, error) {
		n, e := str(id)
		if e != nil {
			return xml.Name{}, e
		}
		if ns == 0xffffffff {
			return xml.Name{Local: n}, nil
		}
		space, e := str(ns)
		if e != nil || space != androidNamespace {
			return xml.Name{}, invalid
		}
		return xml.Name{Space: space, Local: n}, nil
	}
	for offset := 8; offset < len(data); {
		if len(data)-offset < 8 {
			return nil, invalid
		}
		kind := binary.LittleEndian.Uint16(data[offset:])
		header := int(binary.LittleEndian.Uint16(data[offset+2:]))
		size := int(binary.LittleEndian.Uint32(data[offset+4:]))
		if header < 8 || size < header || size > len(data)-offset {
			return nil, invalid
		}
		chunk := data[offset : offset+size]
		switch kind {
		case 1:
			if pool != nil {
				return nil, invalid
			}
			var e error
			pool, e = manifestStrings(chunk)
			if e != nil {
				return nil, e
			}
		case 0x0180, 0x0100, 0x0101: // Resource-name map and namespace markers.
		case 0x0102:
			if header < 16 || size-header < 20 {
				return nil, invalid
			}
			ext := chunk[header:]
			n, e := name(binary.LittleEndian.Uint32(ext), binary.LittleEndian.Uint32(ext[4:]))
			if e != nil {
				return nil, e
			}
			first, stride, count := int(binary.LittleEndian.Uint16(ext[8:])), int(binary.LittleEndian.Uint16(ext[10:])), int(binary.LittleEndian.Uint16(ext[12:]))
			if first < 20 || stride < 20 || first > len(ext) || count > (len(ext)-first)/stride {
				return nil, invalid
			}
			start := xml.StartElement{Name: n}
			for i := 0; i < count; i++ {
				a := ext[first+i*stride:]
				an, e := name(binary.LittleEndian.Uint32(a), binary.LittleEndian.Uint32(a[4:]))
				if e != nil {
					return nil, e
				}
				if binary.LittleEndian.Uint16(a[12:]) != 8 {
					return nil, invalid
				}
				v := binary.LittleEndian.Uint32(a[16:])
				var value string
				switch a[15] {
				case 0:
					value = "@null"
				case 1:
					value = fmt.Sprintf("@0x%08x", v)
					// The pinned Fyne manifest has only these two resource
					// references. AAPT2 source XML requires symbolic names.
					if v == 0x7f020000 {
						value = "@mipmap/icon"
					}
					if v == 0x01030005 {
						value = "@android:style/Theme"
					}
				case 3:
					value, e = str(v)
					if e != nil {
						return nil, e
					}
				case 0x10:
					value = strconv.FormatInt(int64(int32(v)), 10)
				case 0x11:
					value = fmt.Sprintf("0x%08x", v)
					// AAPT2 compiles the location service type to its exact enum
					// bit. Restore only that approved value for semantic validation.
					if an.Space == androidNamespace && an.Local == "foregroundServiceType" && v == 8 {
						value = "location"
					}
					if an.Space == androidNamespace && an.Local == "configChanges" {
						if v != 0x2a0 {
							return nil, errors.New("unsupported Fyne configuration flags")
						}
						value = "keyboardHidden|orientation|uiMode"
					}
				case 0x12:
					if v != 0 {
						value = "true"
					} else {
						value = "false"
					}
				default:
					return nil, invalid
				}
				start.Attr = append(start.Attr, xml.Attr{Name: an, Value: value})
			}
			if len(stack) == 0 {
				if n.Local != "manifest" || len(tokens) != 0 {
					return nil, invalid
				}
			}
			stack = append(stack, n)
			tokens = append(tokens, start)
		case 0x0103:
			if header < 16 || size-header < 8 || len(stack) == 0 {
				return nil, invalid
			}
			n, e := name(binary.LittleEndian.Uint32(chunk[header:]), binary.LittleEndian.Uint32(chunk[header+4:]))
			if e != nil || n != stack[len(stack)-1] {
				return nil, invalid
			}
			tokens = append(tokens, xml.EndElement{Name: n})
			stack = stack[:len(stack)-1]
		default:
			return nil, invalid
		}
		offset += size
	}
	if len(tokens) == 0 || len(stack) != 0 {
		return nil, invalid
	}
	return tokens, nil
}

func manifestStrings(chunk []byte) ([]string, error) {
	invalid := errors.New("invalid manifest string pool")
	if len(chunk) < 28 {
		return nil, invalid
	}
	header := int(binary.LittleEndian.Uint16(chunk[2:]))
	count := int(binary.LittleEndian.Uint32(chunk[8:]))
	flags := binary.LittleEndian.Uint32(chunk[16:])
	start := int(binary.LittleEndian.Uint32(chunk[20:]))
	if header < 28 || header > len(chunk) || count > (len(chunk)-header)/4 || start < header+count*4 || start > len(chunk) {
		return nil, invalid
	}
	out := make([]string, count)
	for i := range out {
		offset := int(binary.LittleEndian.Uint32(chunk[header+i*4:]))
		if offset < 0 || offset >= len(chunk)-start {
			return nil, invalid
		}
		data := chunk[start+offset:]
		if flags&0x100 != 0 {
			length := func() (int, error) {
				if len(data) < 1 {
					return 0, invalid
				}
				n := int(data[0])
				data = data[1:]
				if n&0x80 != 0 {
					if len(data) < 1 {
						return 0, invalid
					}
					n = (n&0x7f)<<8 | int(data[0])
					data = data[1:]
				}
				return n, nil
			}
			if _, e := length(); e != nil {
				return nil, e
			}
			n, e := length()
			if e != nil || n >= len(data) || data[n] != 0 {
				return nil, invalid
			}
			out[i] = string(data[:n])
		} else {
			if len(data) < 2 {
				return nil, invalid
			}
			n := uint32(binary.LittleEndian.Uint16(data))
			data = data[2:]
			if n&0x8000 != 0 {
				if len(data) < 2 {
					return nil, invalid
				}
				n = (n&0x7fff)<<16 | uint32(binary.LittleEndian.Uint16(data))
				data = data[2:]
			}
			if uint64(n)*2+2 > uint64(len(data)) || binary.LittleEndian.Uint16(data[int(n)*2:]) != 0 {
				return nil, invalid
			}
			units := make([]uint16, n)
			for j := range units {
				units[j] = binary.LittleEndian.Uint16(data[j*2:])
			}
			out[i] = string(utf16.Decode(units))
		}
	}
	return out, nil
}

func encodeManifest(tokens []xml.Token) ([]byte, error) {
	var out bytes.Buffer
	out.WriteString(xml.Header)
	encoder := xml.NewEncoder(&out)
	for _, token := range tokens {
		if err := encoder.EncodeToken(token); err != nil {
			return nil, err
		}
	}
	if err := encoder.Flush(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
