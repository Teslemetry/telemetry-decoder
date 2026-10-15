package decoder

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "rewrite the golden files from the decoder's output")

// The fixtures are real payloads from live telemetry, anonymized by
// tools/mkfixtures. Each <name>.request is a decoder protocol request; the
// golden files hold the event for each output option.
var goldenOptions = map[string]Options{
	"flat":                  {Format: FormatFlat},
	"flat.proto-names":      {Format: FormatFlat, ProtoNames: true},
	"protojson":             {Format: FormatProtoJSON},
	"protojson.proto-names": {Format: FormatProtoJSON, ProtoNames: true},
}

func goldenRequests(t testing.TB) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob("testdata/golden/*.request")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixtures in testdata/golden")
	}
	requests := map[string][]byte{}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		requests[strings.TrimSuffix(filepath.Base(path), ".request")] = b
	}
	return requests
}

func TestGolden(t *testing.T) {
	requests := goldenRequests(t)
	types := map[string]bool{}
	for name, request := range requests {
		var r Request
		if err := json.Unmarshal(request, &r); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		types[RecordType(r.Subject)] = true
		for suffix, opts := range goldenOptions {
			t.Run(name+"."+suffix, func(t *testing.T) {
				d, err := New(opts)
				if err != nil {
					t.Fatal(err)
				}
				event, err := d.handle(nil, request)
				if err != nil {
					t.Fatal(err)
				}
				checkEvent(t, event)
				path := filepath.Join("testdata", "golden", name+"."+suffix+".json")
				if *update {
					if err := os.WriteFile(path, append(event, '\n'), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%v (run go test ./internal/decoder -update to create it)", err)
				}
				if got := string(event) + "\n"; got != string(want) {
					t.Errorf("event differs from %s\ngot:  %s\nwant: %s", path, got, want)
				}
			})
		}
	}
	for _, recordType := range []string{"data", "alerts", "errors", "connectivity"} {
		if !types[recordType] {
			t.Errorf("no fixture for %s", recordType)
		}
	}
}

// checkEvent fails unless event is one line of valid UTF-8 holding a JSON object.
func checkEvent(t *testing.T, event []byte) {
	t.Helper()
	if bytes.ContainsAny(event, "\r\n") {
		t.Errorf("event is not one line: %q", event)
	}
	if !utf8.Valid(event) {
		t.Errorf("event is not valid UTF-8: %q", event)
	}
	var object map[string]any
	if err := json.Unmarshal(event, &object); err != nil {
		t.Errorf("event is not a JSON object: %v: %s", err, event)
	}
}
