package decoder

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Teslemetry/telemetry-decoder/internal/protos"
)

func mustNew(t testing.TB, opts Options) *Decoder {
	t.Helper()
	d, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func decode(t *testing.T, d *Decoder, subject string, message proto.Message) string {
	t.Helper()
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	event, err := d.Decode(nil, subject, payload)
	if err != nil {
		t.Fatal(err)
	}
	checkEvent(t, event)
	return string(event)
}

func datum(key protos.Field, value *protos.Value) *protos.Datum {
	return &protos.Datum{Key: key, Value: value}
}

func TestFlatValues(t *testing.T) {
	p := &protos.Payload{
		Vin:       "TESTVIN0000000001",
		CreatedAt: &timestamppb.Timestamp{Seconds: 1792000000, Nanos: 500000000},
		Data: []*protos.Datum{
			datum(protos.Field_VehicleSpeed, &protos.Value{Value: &protos.Value_FloatValue{FloatValue: 28.6}}),
			datum(protos.Field_Odometer, &protos.Value{Value: &protos.Value_DoubleValue{DoubleValue: 12345.678}}),
			datum(protos.Field_ChargeAmps, &protos.Value{Value: &protos.Value_IntValue{IntValue: -16}}),
			datum(protos.Field_LifetimeEnergyUsed, &protos.Value{Value: &protos.Value_LongValue{LongValue: 1 << 40}}),
			datum(protos.Field_Locked, &protos.Value{Value: &protos.Value_BooleanValue{BooleanValue: true}}),
			datum(protos.Field_VehicleName, &protos.Value{Value: &protos.Value_StringValue{StringValue: "Say \"hi\"\n\u2028"}}),
			datum(protos.Field_Gear, &protos.Value{Value: &protos.Value_ShiftStateValue{ShiftStateValue: protos.ShiftState_ShiftStateD}}),
			datum(protos.Field_Location, &protos.Value{Value: &protos.Value_LocationValue{LocationValue: &protos.LocationValue{Latitude: 37.4919, Longitude: -121.9447}}}),
			datum(protos.Field_DoorState, &protos.Value{Value: &protos.Value_DoorValue{DoorValue: &protos.Doors{DriverFront: true}}}),
			datum(protos.Field_ScheduledChargingStartTime, &protos.Value{Value: &protos.Value_TimeValue{TimeValue: &protos.Time{Hour: 7}}}),
			datum(protos.Field_TpmsPressureFl, &protos.Value{Value: &protos.Value_Invalid{Invalid: true}}),
			datum(protos.Field_TpmsPressureFr, &protos.Value{Value: &protos.Value_Invalid{Invalid: true}}),
		},
	}
	got := decode(t, mustNew(t, Options{}), "telemetry.TESTVIN0000000001.data", p)
	want := `{"vin":"TESTVIN0000000001","createdAt":"2026-10-14T17:46:40.500Z","isResend":false,` +
		`"VehicleSpeed":28.6,"Odometer":12345.678,"ChargeAmps":-16,"LifetimeEnergyUsed":1099511627776,` +
		`"Locked":true,"VehicleName":"Say \"hi\"\n\u2028","Gear":"ShiftStateD",` +
		`"Location":{"latitude":37.4919,"longitude":-121.9447},` +
		`"DoorState":{"DriverFront":true,"DriverRear":false,"PassengerFront":false,"PassengerRear":false,"TrunkFront":false,"TrunkRear":false},` +
		`"ScheduledChargingStartTime":{"hour":7,"minute":0,"second":0},` +
		`"invalid":["TpmsPressureFl","TpmsPressureFr"]}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestFlatProtoNames(t *testing.T) {
	p := &protos.Payload{
		IsResend: true,
		Data: []*protos.Datum{
			datum(protos.Field_TpmsHardWarnings, &protos.Value{Value: &protos.Value_TireLocationValue{TireLocationValue: &protos.TireLocation{FrontLeft: true}}}),
		},
	}
	got := decode(t, mustNew(t, Options{ProtoNames: true}), "telemetry.TESTVIN0000000001.data", p)
	if !strings.HasPrefix(got, `{"vin":"TESTVIN0000000001","created_at":null,"is_resend":true,"TpmsHardWarnings":{"front_left":true,"front_right":false,`) {
		t.Errorf("got %s", got)
	}
}

func TestFlatDuplicateSignalKeepsLast(t *testing.T) {
	p := &protos.Payload{Data: []*protos.Datum{
		datum(protos.Field_VehicleSpeed, &protos.Value{Value: &protos.Value_FloatValue{FloatValue: 1}}),
		datum(protos.Field_Soc, &protos.Value{Value: &protos.Value_FloatValue{FloatValue: 50}}),
		datum(protos.Field_VehicleSpeed, &protos.Value{Value: &protos.Value_FloatValue{FloatValue: 2}}),
	}}
	got := decode(t, mustNew(t, Options{}), "telemetry.V.data", p)
	if want := `{"vin":"V","createdAt":null,"isResend":false,"Soc":50,"VehicleSpeed":2}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// Vehicles can send signals and value types newer than the decoder's protos.
func TestFlatNewerThanProtos(t *testing.T) {
	var value []byte
	value = protowire.AppendTag(value, 999, protowire.VarintType) // an unknown value type
	value = protowire.AppendVarint(value, 1)
	var unknownValue []byte
	unknownValue = protowire.AppendTag(unknownValue, 1, protowire.VarintType)
	unknownValue = protowire.AppendVarint(unknownValue, uint64(protos.Field_VehicleSpeed))
	unknownValue = protowire.AppendTag(unknownValue, 2, protowire.BytesType)
	unknownValue = protowire.AppendBytes(unknownValue, value)

	var enumValue []byte
	enumValue = protowire.AppendTag(enumValue, 9, protowire.VarintType) // shift_state_value
	enumValue = protowire.AppendVarint(enumValue, 77)
	var unknownField []byte
	unknownField = protowire.AppendTag(unknownField, 1, protowire.VarintType)
	unknownField = protowire.AppendVarint(unknownField, 4321)
	unknownField = protowire.AppendTag(unknownField, 2, protowire.BytesType)
	unknownField = protowire.AppendBytes(unknownField, enumValue)

	var payload []byte
	payload = protowire.AppendTag(payload, 1, protowire.BytesType)
	payload = protowire.AppendBytes(payload, unknownValue)
	payload = protowire.AppendTag(payload, 1, protowire.BytesType)
	payload = protowire.AppendBytes(payload, unknownField)

	event, err := mustNew(t, Options{}).Decode(nil, "telemetry.V.data", payload)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(event), `{"vin":"V","createdAt":null,"isResend":false,"VehicleSpeed":null,"4321":77}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestProtoJSONPayload(t *testing.T) {
	p := &protos.Payload{
		Vin: "TESTVIN0000000001",
		Data: []*protos.Datum{
			datum(protos.Field_VehicleSpeed, &protos.Value{Value: &protos.Value_FloatValue{FloatValue: 28.6}}),
		},
	}
	got := decode(t, mustNew(t, Options{Format: FormatProtoJSON}), "telemetry.TESTVIN0000000001.data", p)
	want := `{"data":[{"key":"VehicleSpeed","value":{"floatValue":28.6}}],"createdAt":null,"vin":"TESTVIN0000000001","isResend":false}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestRecordTypes(t *testing.T) {
	d := mustNew(t, Options{})
	for _, tc := range []struct {
		subject string
		message proto.Message
		want    string
	}{
		{"telemetry.V.alerts", &protos.VehicleAlerts{Alerts: []*protos.VehicleAlert{{Name: "A", Audiences: []protos.Audience{protos.Audience_Customer}}}},
			`{"vin":"V","createdAt":null,"alerts":[{"name":"A","audiences":["Customer"],"startedAt":null,"endedAt":null}]}`},
		{"telemetry.V.errors", &protos.VehicleErrors{Errors: []*protos.VehicleError{{Name: "E", Tags: map[string]string{"k": "v"}}}},
			`{"vin":"V","createdAt":null,"errors":[{"createdAt":null,"name":"E","tags":{"k":"v"},"body":""}]}`},
		{"telemetry.V.connectivity", &protos.VehicleConnectivity{Status: protos.ConnectivityEvent_CONNECTED, NetworkInterface: "wifi"},
			`{"vin":"V","createdAt":null,"connectionId":"","status":"CONNECTED","networkInterface":"wifi"}`},
		{"telemetry.V.metrics", &protos.VehicleMetrics{Metrics: []*protos.Metric{{Name: "m", Value: 1.5}}},
			`{"vin":"V","createdAt":null,"metrics":[{"name":"m","tags":{},"value":1.5}]}`},
		// The VIN in the message wins over the subject
		{"telemetry.OTHER.connectivity", &protos.VehicleConnectivity{Vin: "V", Status: protos.ConnectivityEvent_DISCONNECTED},
			`{"vin":"V","createdAt":null,"connectionId":"","status":"DISCONNECTED","networkInterface":""}`},
		// No VIN anywhere
		{"connectivity", &protos.VehicleConnectivity{Status: protos.ConnectivityEvent_DISCONNECTED},
			`{"vin":"","createdAt":null,"connectionId":"","status":"DISCONNECTED","networkInterface":""}`},
	} {
		if got := decode(t, d, tc.subject, tc.message); got != tc.want {
			t.Errorf("%s:\ngot  %s\nwant %s", tc.subject, got, tc.want)
		}
	}
}

func TestDecodeErrors(t *testing.T) {
	d := mustNew(t, Options{})
	for _, tc := range []struct {
		subject string
		payload []byte
		want    string
	}{
		{"telemetry.V.unknown", []byte{0x08, 0x01}, `unknown record type "unknown"`},
		{"", []byte{0x08, 0x01}, `unknown record type ""`},
		{"telemetry.V.data", nil, "empty payload"},
		{"telemetry.V.data", []byte("not a protobuf"), "cannot decode data payload"},
	} {
		dst := []byte("prefix")
		got, err := d.Decode(dst, tc.subject, tc.payload)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %q: error %v, want %q", tc.subject, tc.payload, err, tc.want)
		}
		if string(got) != "prefix" {
			t.Errorf("%s %q: dst changed to %q", tc.subject, tc.payload, got)
		}
	}
	if _, err := New(Options{Format: "xml"}); err == nil {
		t.Error("New accepted an unknown format")
	}
}

func TestAppendFloat(t *testing.T) {
	for _, tc := range []struct {
		f    float64
		bits int
		want string
	}{
		{float64(float32(28.6)), 32, "28.6"},
		{28.6, 64, "28.6"},
		{0, 64, "0"},
		{-0.5, 32, "-0.5"},
		{1e-7, 64, "1e-7"},
		{1e21, 64, "1e+21"},
		{123456789, 64, "123456789"},
		{math.NaN(), 64, `"NaN"`},
		{math.Inf(1), 32, `"Infinity"`},
		{math.Inf(-1), 64, `"-Infinity"`},
	} {
		if got := string(appendFloat(nil, tc.f, tc.bits)); got != tc.want {
			t.Errorf("appendFloat(%v, %d) = %s, want %s", tc.f, tc.bits, got, tc.want)
		}
	}
}

func TestAppendString(t *testing.T) {
	for _, s := range []string{"", "plain", "quote\" backslash\\ tab\t nl\n cr\r", "\x00\x1f\u2028\u2029", "héllo 🚗", "bad \xff utf8"} {
		var got string
		if err := json.Unmarshal(appendString(nil, s), &got); err != nil {
			t.Errorf("appendString(%q) is not a JSON string: %v", s, err)
			continue
		}
		want := strings.ToValidUTF8(s, "\uFFFD")
		if got != want {
			t.Errorf("appendString(%q) decodes to %q, want %q", s, got, want)
		}
	}
}
