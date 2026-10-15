// Package decoder turns Tesla Fleet Telemetry protobuf payloads into JSON events.
package decoder

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/Teslemetry/telemetry-decoder/internal/protos"
)

// Format selects the shape of a decoded vehicle data event.
type Format string

const (
	// FormatFlat puts each vehicle data signal in its own top-level field
	// ("VehicleSpeed": 28.6) so Splunk extracts it directly.
	FormatFlat Format = "flat"
	// FormatProtoJSON is the protobuf JSON mapping, as fleet-telemetry writes it
	// with transmit_decoded_records: data is a list of {"key", "value"} pairs.
	FormatProtoJSON Format = "protojson"
)

// Options controls how payloads are decoded.
type Options struct {
	Format Format
	// ProtoNames uses the proto field names (created_at) instead of the JSON
	// names (createdAt).
	ProtoNames bool
}

// Record types, named by the last token of the subject the payload is published
// on (telemetry.<VIN>.data and so on), as fleet-telemetry names them.
var recordTypes = map[string]func() proto.Message{
	"data":         func() proto.Message { return &protos.Payload{} },
	"alerts":       func() proto.Message { return &protos.VehicleAlerts{} },
	"errors":       func() proto.Message { return &protos.VehicleErrors{} },
	"connectivity": func() proto.Message { return &protos.VehicleConnectivity{} },
	"metrics":      func() proto.Message { return &protos.VehicleMetrics{} },
}

// Decoder decodes payloads into one-line JSON events.
type Decoder struct {
	format  Format
	json    protojson.MarshalOptions
	inner   protojson.MarshalOptions // for the messages inside a flattened data value
	names   fieldNames
	scratch bytes.Buffer
}

type fieldNames struct {
	vin, createdAt, isResend string
}

// New returns a Decoder for the options.
func New(opts Options) (*Decoder, error) {
	switch opts.Format {
	case "":
		opts.Format = FormatFlat
	case FormatFlat, FormatProtoJSON:
	default:
		return nil, fmt.Errorf("unknown format %q: use %q or %q", opts.Format, FormatFlat, FormatProtoJSON)
	}
	d := &Decoder{
		format: opts.Format,
		// Every field is written, even when unset, as fleet-telemetry does: an
		// alert without endedAt is still active, a door that is false is closed.
		json:  protojson.MarshalOptions{EmitUnpopulated: true, UseProtoNames: opts.ProtoNames},
		inner: protojson.MarshalOptions{EmitUnpopulated: true, UseProtoNames: opts.ProtoNames},
		names: fieldNames{"vin", "createdAt", "isResend"},
	}
	if opts.ProtoNames {
		d.names = fieldNames{"vin", "created_at", "is_resend"}
	}
	return d, nil
}

// RecordType returns the record type a subject carries: its last token.
func RecordType(subject string) string {
	return subject[strings.LastIndexByte(subject, '.')+1:]
}

// Decode decodes a payload published on subject and appends the JSON event,
// without a newline, to dst.
func (d *Decoder) Decode(dst []byte, subject string, payload []byte) ([]byte, error) {
	recordType := RecordType(subject)
	newMessage, ok := recordTypes[recordType]
	if !ok {
		return dst, fmt.Errorf("unknown record type %q (subject %q): expected one of data, alerts, errors, connectivity, metrics", recordType, subject)
	}
	if len(payload) == 0 {
		return dst, errors.New("empty payload")
	}
	message := newMessage()
	if err := proto.Unmarshal(payload, message); err != nil {
		return dst, fmt.Errorf("cannot decode %s payload: %w", recordType, err)
	}
	fillVIN(message, subject)
	if d.format == FormatProtoJSON {
		return d.appendProtoJSON(dst, message)
	}
	if payload, ok := message.(*protos.Payload); ok {
		return d.appendFlatPayload(dst, payload)
	}
	return d.appendIdentityFirst(dst, message)
}

// fillVIN sets the VIN from the subject (telemetry.<VIN>.<type>) when the
// message has none.
func fillVIN(message proto.Message, subject string) {
	m := message.ProtoReflect()
	field := m.Descriptor().Fields().ByName("vin")
	if field == nil || m.Get(field).String() != "" {
		return
	}
	tokens := strings.Split(subject, ".")
	if len(tokens) == 3 && tokens[1] != "" {
		m.Set(field, protoreflect.ValueOfString(tokens[1]))
	}
}

// appendProtoJSON appends the message in the protobuf JSON mapping, compacted:
// protojson varies its whitespace on purpose, and an event must be one stable line.
func (d *Decoder) appendProtoJSON(dst []byte, message proto.Message) ([]byte, error) {
	b, err := d.json.Marshal(message)
	if err != nil {
		return dst, err
	}
	return appendCompact(dst, &d.scratch, b)
}

// appendIdentityFirst appends the message in the protobuf JSON mapping with vin
// and createdAt as its first fields. Alerts and errors list them after a long
// array (alert events reach 16 KB), past the point where Splunk stops extracting
// fields at search time.
func (d *Decoder) appendIdentityFirst(dst []byte, message proto.Message) ([]byte, error) {
	b, err := d.json.Marshal(message)
	if err != nil {
		return dst, err
	}
	type member struct {
		key   string
		value json.RawMessage
	}
	var members []member
	dec := json.NewDecoder(bytes.NewReader(b))
	if _, err := dec.Token(); err != nil { // {
		return dst, err
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return dst, err
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return dst, err
		}
		members = append(members, member{key.(string), value})
	}
	ordered := make([]byte, 0, len(b))
	ordered = append(ordered, '{')
	write := func(m member) {
		if len(ordered) > 1 {
			ordered = append(ordered, ',')
		}
		ordered = append(appendKey(ordered, m.key), m.value...)
	}
	for _, first := range []string{d.names.vin, d.names.createdAt} {
		for _, m := range members {
			if m.key == first {
				write(m)
			}
		}
	}
	for _, m := range members {
		if m.key != d.names.vin && m.key != d.names.createdAt {
			write(m)
		}
	}
	return appendCompact(dst, &d.scratch, append(ordered, '}'))
}

func appendCompact(dst []byte, scratch *bytes.Buffer, b []byte) ([]byte, error) {
	scratch.Reset()
	if err := json.Compact(scratch, b); err != nil {
		return dst, err
	}
	return append(dst, scratch.Bytes()...), nil
}

// appendFlatPayload appends a vehicle data payload with one field per signal:
//
//	{"vin":"...","createdAt":"...","isResend":false,"VehicleSpeed":28.6,"Location":{"latitude":..,"longitude":..}}
//
// Signals the vehicle reports as invalid are listed in "invalid" instead. A signal
// repeated in one payload keeps its last value.
func (d *Decoder) appendFlatPayload(dst []byte, p *protos.Payload) ([]byte, error) {
	start := len(dst)
	dst = append(dst, '{')
	dst = appendKey(dst, d.names.vin)
	dst = appendString(dst, p.GetVin())
	dst = append(dst, ',')
	dst = appendKey(dst, d.names.createdAt)
	if p.GetCreatedAt() == nil {
		dst = append(dst, "null"...)
	} else {
		var err error
		if dst, err = d.appendInner(dst, p.GetCreatedAt()); err != nil {
			return dst[:start], err
		}
	}
	dst = append(dst, ',')
	dst = appendKey(dst, d.names.isResend)
	dst = strconv.AppendBool(dst, p.GetIsResend())

	data := p.GetData()
	last := make(map[protos.Field]int, len(data))
	for i, datum := range data {
		last[datum.GetKey()] = i
	}
	var invalid []string
	for i, datum := range data {
		if last[datum.GetKey()] != i {
			continue
		}
		name := fieldName(datum.GetKey())
		if datum.GetValue().GetInvalid() {
			invalid = append(invalid, name)
			continue
		}
		dst = append(dst, ',')
		dst = appendKey(dst, name)
		var err error
		if dst, err = d.appendValue(dst, datum.GetValue()); err != nil {
			return dst[:start], fmt.Errorf("%s: %w", name, err)
		}
	}
	if len(invalid) > 0 {
		dst = append(dst, `,"invalid":[`...)
		for i, name := range invalid {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendString(dst, name)
		}
		dst = append(dst, ']')
	}
	return append(dst, '}'), nil
}

// fieldName is the signal's name in the Field enum, or its number when this
// decoder's protos do not know it yet.
func fieldName(f protos.Field) string {
	if v := f.Descriptor().Values().ByNumber(protoreflect.EnumNumber(f)); v != nil {
		return string(v.Name())
	}
	return strconv.Itoa(int(f))
}

var valueOneof = (&protos.Value{}).ProtoReflect().Descriptor().Oneofs().ByName("value")

// appendValue appends a signal's value as plain JSON: numbers, strings and
// booleans as themselves, enums by name, and messages (Location, Doors, ...) as
// objects with every field.
func (d *Decoder) appendValue(dst []byte, v *protos.Value) ([]byte, error) {
	m := v.ProtoReflect()
	field := m.WhichOneof(valueOneof)
	if field == nil {
		// No value, or a value type newer than this decoder's protos
		return append(dst, "null"...), nil
	}
	value := m.Get(field)
	switch field.Kind() {
	case protoreflect.StringKind:
		return appendString(dst, value.String()), nil
	case protoreflect.BoolKind:
		return strconv.AppendBool(dst, value.Bool()), nil
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Sint32Kind, protoreflect.Sint64Kind,
		protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind:
		return strconv.AppendInt(dst, value.Int(), 10), nil
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind, protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
		return strconv.AppendUint(dst, value.Uint(), 10), nil
	case protoreflect.FloatKind:
		return appendFloat(dst, value.Float(), 32), nil
	case protoreflect.DoubleKind:
		return appendFloat(dst, value.Float(), 64), nil
	case protoreflect.EnumKind:
		if name := field.Enum().Values().ByNumber(value.Enum()); name != nil {
			return appendString(dst, string(name.Name())), nil
		}
		return strconv.AppendInt(dst, int64(value.Enum()), 10), nil
	case protoreflect.MessageKind:
		return d.appendInner(dst, value.Message().Interface())
	default:
		return dst, fmt.Errorf("unsupported value type %s", field.Kind())
	}
}

func (d *Decoder) appendInner(dst []byte, message proto.Message) ([]byte, error) {
	b, err := d.inner.Marshal(message)
	if err != nil {
		return dst, err
	}
	return appendCompact(dst, &d.scratch, b)
}

func appendKey(dst []byte, key string) []byte {
	return append(appendString(dst, key), ':')
}

// appendFloat formats a float as encoding/json does, with the shortest
// representation for its size (a float32 28.6 is 28.6, not 28.600000381469727).
// JSON has no NaN or infinity, so those are written as protojson writes them.
func appendFloat(dst []byte, f float64, bits int) []byte {
	switch {
	case math.IsNaN(f):
		return append(dst, `"NaN"`...)
	case math.IsInf(f, 1):
		return append(dst, `"Infinity"`...)
	case math.IsInf(f, -1):
		return append(dst, `"-Infinity"`...)
	}
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 {
		if bits == 64 && (abs < 1e-6 || abs >= 1e21) || bits == 32 && (float32(abs) < 1e-6 || float32(abs) >= 1e21) {
			format = 'e'
		}
	}
	dst = strconv.AppendFloat(dst, f, format, -1, bits)
	if format == 'e' {
		// clean up e-09 to e-9
		n := len(dst)
		if n >= 4 && dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
			dst[n-2] = dst[n-1]
			dst = dst[:n-1]
		}
	}
	return dst
}

const hex = "0123456789abcdef"

// appendString appends s as a JSON string. Invalid UTF-8 becomes U+FFFD, so the
// event is always valid UTF-8 (the add-on rejects anything else).
func appendString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			dst = append(dst, '\\', byte(r))
		case r == '\n':
			dst = append(dst, '\\', 'n')
		case r == '\r':
			dst = append(dst, '\\', 'r')
		case r == '\t':
			dst = append(dst, '\\', 't')
		case r < 0x20 || r == ' ' || r == ' ':
			dst = append(dst, '\\', 'u', hex[r>>12&0xf], hex[r>>8&0xf], hex[r>>4&0xf], hex[r&0xf])
		default:
			dst = utf8.AppendRune(dst, r)
		}
	}
	return append(dst, '"')
}
