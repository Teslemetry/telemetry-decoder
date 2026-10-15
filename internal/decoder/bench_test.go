package decoder

import (
	"encoding/json"
	"io"
	"sort"
	"testing"
)

// BenchmarkDecode decodes each fixture payload.
func BenchmarkDecode(b *testing.B) {
	requests := goldenRequests(b)
	names := make([]string, 0, len(requests))
	for name := range requests {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, format := range []Format{FormatFlat, FormatProtoJSON} {
		for _, name := range names {
			var r Request
			if err := json.Unmarshal(requests[name], &r); err != nil {
				b.Fatal(err)
			}
			payload, err := r.DecodePayload()
			if err != nil {
				b.Fatal(err)
			}
			b.Run(string(format)+"/"+name, func(b *testing.B) {
				d := mustNew(b, Options{Format: format})
				var event []byte
				b.SetBytes(int64(len(payload)))
				for b.Loop() {
					var err error
					if event, err = d.Decode(event[:0], r.Subject, payload); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkRun serves the decoder protocol over a stream cycling through every
// fixture, as the add-on sends it: one request line in, one event line out,
// flushed. One op is one message, so messages per second is 1e9 / ns/op.
func BenchmarkRun(b *testing.B) {
	var lines [][]byte
	for _, request := range goldenRequests(b) {
		lines = append(lines, request)
	}
	d := mustNew(b, Options{})
	in := &cyclingReader{lines: lines, remaining: b.N}
	var out countingWriter
	b.ResetTimer()
	if err := d.Run(in, &out, io.Discard); err != nil {
		b.Fatal(err)
	}
	b.StopTimer()
	if out.lines != b.N || out.empty != 0 {
		b.Fatalf("%d events (%d empty) for %d requests", out.lines, out.empty, b.N)
	}
}

// cyclingReader reads remaining request lines, cycling through lines.
type cyclingReader struct {
	lines     [][]byte
	remaining int
	next      int
	pending   []byte
}

func (r *cyclingReader) Read(p []byte) (int, error) {
	if len(r.pending) == 0 {
		if r.remaining == 0 {
			return 0, io.EOF
		}
		r.pending = r.lines[r.next%len(r.lines)]
		r.next++
		r.remaining--
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// countingWriter counts the event lines written, and the empty ones.
type countingWriter struct {
	lines, empty int
	inLine       bool
}

func (w *countingWriter) Write(p []byte) (int, error) {
	for _, c := range p {
		if c == '\n' {
			w.lines++
			if !w.inLine {
				w.empty++
			}
			w.inLine = false
		} else {
			w.inLine = true
		}
	}
	return len(p), nil
}
