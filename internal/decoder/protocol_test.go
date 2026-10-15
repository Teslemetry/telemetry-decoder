package decoder

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/Teslemetry/telemetry-decoder/internal/protos"
)

func request(subject string, payload []byte) string {
	b, _ := json.Marshal(map[string]any{"subject": subject, "headers": map[string]string{}, "payload": payload})
	return string(b) + "\n"
}

func connectivityPayload(t testing.TB) []byte {
	payload, err := proto.Marshal(&protos.VehicleConnectivity{Vin: "V", Status: protos.ConnectivityEvent_CONNECTED})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func run(t *testing.T, input string) (out []string, diag string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := mustNew(t, Options{}).Run(strings.NewReader(input), &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stdout.Len() > 0 && !bytes.HasSuffix(stdout.Bytes(), []byte("\n")) {
		t.Errorf("output does not end with a newline: %q", stdout.String())
	}
	return strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n"), stderr.String()
}

func TestProtocol(t *testing.T) {
	good := request("telemetry.V.connectivity", connectivityPayload(t))
	wantGood := `{"vin":"V","createdAt":null,"connectionId":"","status":"CONNECTED","networkInterface":""}`
	for _, tc := range []struct {
		name     string
		request  string
		wantDiag string
	}{
		{"bad base64", `{"subject":"telemetry.V.data","headers":{},"payload":"not base64!"}` + "\n", "invalid payload: illegal base64 data"},
		{"unknown subject", request("telemetry.V.other", connectivityPayload(t)), `telemetry.V.other: unknown record type "other"`},
		{"empty payload", request("telemetry.V.data", nil), "telemetry.V.data: empty payload"},
		{"missing payload", `{"subject":"telemetry.V.data","headers":{}}` + "\n", "telemetry.V.data: empty payload"},
		{"not a protobuf", request("telemetry.V.data", []byte("\xff\xff\xff")), "telemetry.V.data: cannot decode data payload"},
		{"not JSON", "hello\n", "invalid request"},
		{"empty line", "\n", "empty request line"},
		{"CRLF", strings.TrimSuffix(good, "\n") + "\r\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A bad request gets an empty line and leaves the decoder serving the next
			out, diag := run(t, tc.request+good)
			want := []string{"", wantGood}
			if tc.wantDiag == "" {
				want = []string{wantGood, wantGood}
			}
			if fmt.Sprint(out) != fmt.Sprint(want) {
				t.Errorf("output %q, want %q", out, want)
			}
			if tc.wantDiag == "" && diag != "" || !strings.Contains(diag, tc.wantDiag) {
				t.Errorf("stderr %q, want %q", diag, tc.wantDiag)
			}
			if strings.Count(diag, "\n") > 1 {
				t.Errorf("more than one diagnostic line: %q", diag)
			}
		})
	}
}

func TestProtocolEOF(t *testing.T) {
	good := request("telemetry.V.connectivity", connectivityPayload(t))
	// No input: no output
	if out, _ := run(t, ""); fmt.Sprint(out) != "[]" {
		t.Errorf("output %q for no input", out)
	}
	// A last request without a newline is still answered
	out, _ := run(t, good+strings.TrimSuffix(good, "\n"))
	if len(out) != 2 || out[1] == "" {
		t.Errorf("output %q, want two events", out)
	}
}

func TestProtocolLongRequest(t *testing.T) {
	// A request far beyond the read buffer
	p := &protos.Payload{Vin: "V"}
	for range 5000 {
		p.Data = append(p.Data, &protos.Datum{Key: protos.Field_VehicleName,
			Value: &protos.Value{Value: &protos.Value_StringValue{StringValue: strings.Repeat("x", 100)}}})
	}
	payload, err := proto.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	input := request("telemetry.V.data", payload)
	if len(input) < 512*1024 {
		t.Fatalf("request is only %d bytes", len(input))
	}
	out, diag := run(t, input+input)
	if len(out) != 2 || !strings.Contains(out[0], `"VehicleName":"xxx`) || out[0] != out[1] {
		t.Errorf("unexpected output for long requests (%d lines), stderr %q", len(out), diag)
	}
}

// Each event is flushed as soon as it is written: the add-on waits for it before
// sending the next request.
func TestProtocolFlushesEachLine(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- mustNew(t, Options{}).Run(inR, outW, io.Discard)
		_ = outW.Close()
	}()
	reader := bufio.NewReader(outR)
	good := request("telemetry.V.connectivity", connectivityPayload(t))
	for i := range 3 {
		if _, err := io.WriteString(inW, good); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if !strings.Contains(line, `"status":"CONNECTED"`) {
			t.Fatalf("request %d: got %q", i, line)
		}
	}
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatalf("Run after stdin closed: %v", err)
	}
}

func TestRequestPayloadIsStandardBase64(t *testing.T) {
	payload := connectivityPayload(t)
	line := fmt.Sprintf(`{"subject":"telemetry.V.connectivity","headers":{"traceparent":"00-1-2-00"},"payload":%q}`,
		base64.StdEncoding.EncodeToString(payload))
	event, err := mustNew(t, Options{}).handle(nil, []byte(line))
	if err != nil || !strings.Contains(string(event), "CONNECTED") {
		t.Errorf("event %q, error %v", event, err)
	}
}
