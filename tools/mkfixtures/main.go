// mkfixtures picks representative payloads from a capture of live telemetry
// traffic and writes them, anonymized, as test fixtures.
//
// The capture is decoder protocol requests, one per line, as the NATS add-on
// sends them. The fixtures replace the VIN, locations, names, media and
// connection IDs with fixed fake values, so no real vehicle can be identified.
//
// Usage: go run ./tools/mkfixtures -out internal/decoder/testdata/golden < capture.jsonl
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/Teslemetry/telemetry-decoder/internal/decoder"
	"github.com/Teslemetry/telemetry-decoder/internal/protos"
)

const (
	fakeVIN          = "TESTVIN0000000001"
	fakeConnectionID = "00000000-0000-4000-8000-000000000001"
	fakeTraceparent  = "00-00000000000000000000000000000001-0000000000000001-00"
)

var (
	vinPattern  = regexp.MustCompile(`\b[A-HJ-NPR-Z0-9]{17}\b`)
	uuidPattern = regexp.MustCompile(`[0-9a-f]{8,12}-?[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	// Signals that can identify a person or a place
	personalStrings = map[protos.Field]string{
		protos.Field_VehicleName:            "Test Vehicle",
		protos.Field_DestinationName:        "Tesla Fremont Factory",
		protos.Field_RouteLine:              "",
		protos.Field_MediaNowPlayingTitle:   "Test Title",
		protos.Field_MediaNowPlayingArtist:  "Test Artist",
		protos.Field_MediaNowPlayingAlbum:   "Test Album",
		protos.Field_MediaNowPlayingStation: "Test Station",
	}
)

type candidate struct {
	name    string
	payload []byte
	subject string
}

func main() {
	out := flag.String("out", "internal/decoder/testdata/golden", "fixture directory")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "mkfixtures:", err)
		os.Exit(1)
	}
}

func run(out string) error {
	picked := map[string]candidate{}
	// pick keeps the first payload for a name, or the better one by less
	pick := func(name string, c candidate, better bool) {
		if _, ok := picked[name]; !ok || better {
			c.name = name
			picked[name] = c
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(nil, 16<<20)
	for scanner.Scan() {
		var r decoder.Request
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			return err
		}
		payload, err := r.DecodePayload()
		if err != nil {
			return err
		}
		c := candidate{payload: payload, subject: r.Subject}
		switch decoder.RecordType(r.Subject) {
		case "data":
			var p protos.Payload
			if proto.Unmarshal(payload, &p) != nil {
				continue
			}
			best, ok := picked["data-full"]
			pick("data-full", c, ok && len(p.Data) > countData(best.payload))
			for _, d := range p.Data {
				switch {
				case d.GetValue().GetInvalid():
					pick("data-invalid", c, false)
				case d.GetKey() == protos.Field_Location:
					pick("data-location", c, false)
				case d.GetValue().GetDoorValue() != nil:
					pick("data-doors", c, false)
				}
			}
			if p.IsResend {
				pick("data-resend", c, false)
			}
		case "alerts":
			var a protos.VehicleAlerts
			if proto.Unmarshal(payload, &a) != nil {
				continue
			}
			for _, alert := range a.Alerts {
				if alert.EndedAt == nil {
					// the smallest payload with an active alert
					best, ok := picked["alerts"]
					pick("alerts", c, ok && len(c.payload) < len(best.payload))
					break
				}
			}
		case "errors":
			var e protos.VehicleErrors
			if proto.Unmarshal(payload, &e) != nil {
				continue
			}
			if len(e.Errors) > 0 {
				pick("errors", c, false)
			} else {
				pick("errors-empty", c, false)
			}
		case "connectivity":
			var v protos.VehicleConnectivity
			if proto.Unmarshal(payload, &v) != nil {
				continue
			}
			pick("connectivity-"+strings.ToLower(v.Status.String()), c, false)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	names := make([]string, 0, len(picked))
	for name := range picked {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := picked[name]
		payload, err := anonymize(c.subject, c.payload)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		recordType := decoder.RecordType(c.subject)
		request, err := json.Marshal(map[string]any{
			"subject": "telemetry." + fakeVIN + "." + recordType,
			"headers": map[string]string{"traceparent": fakeTraceparent},
			"payload": payload,
		})
		if err != nil {
			return err
		}
		if vinPattern.Match(payload) && !strings.Contains(string(payload), fakeVIN) {
			return fmt.Errorf("%s: payload still contains a VIN-like string", name)
		}
		path := filepath.Join(out, name+".request")
		if err := os.WriteFile(path, append(request, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", path)
	}
	return nil
}

func countData(payload []byte) int {
	var p protos.Payload
	_ = proto.Unmarshal(payload, &p)
	return len(p.Data)
}

func anonymize(subject string, payload []byte) ([]byte, error) {
	var message proto.Message
	switch decoder.RecordType(subject) {
	case "data":
		p := &protos.Payload{}
		if err := proto.Unmarshal(payload, p); err != nil {
			return nil, err
		}
		p.Vin = fakeVIN
		for _, d := range p.Data {
			if loc := d.GetValue().GetLocationValue(); loc != nil {
				// Tesla Fremont Factory
				loc.Latitude, loc.Longitude = 37.4919, -121.9447
			}
			if fake, ok := personalStrings[d.GetKey()]; ok && d.GetValue().GetStringValue() != "" {
				d.Value = &protos.Value{Value: &protos.Value_StringValue{StringValue: fake}}
			}
		}
		message = p
	case "alerts":
		a := &protos.VehicleAlerts{}
		if err := proto.Unmarshal(payload, a); err != nil {
			return nil, err
		}
		a.Vin = fakeVIN
		message = a
	case "errors":
		e := &protos.VehicleErrors{}
		if err := proto.Unmarshal(payload, e); err != nil {
			return nil, err
		}
		e.Vin = fakeVIN
		for _, err := range e.Errors {
			for k, v := range err.Tags {
				err.Tags[k] = uuidPattern.ReplaceAllString(vinPattern.ReplaceAllString(v, fakeVIN), fakeConnectionID)
			}
			err.Body = vinPattern.ReplaceAllString(err.Body, fakeVIN)
		}
		message = e
	case "connectivity":
		v := &protos.VehicleConnectivity{}
		if err := proto.Unmarshal(payload, v); err != nil {
			return nil, err
		}
		v.Vin = fakeVIN
		v.ConnectionId = fakeConnectionID
		message = v
	default:
		return nil, fmt.Errorf("unknown subject %q", subject)
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(message)
}
