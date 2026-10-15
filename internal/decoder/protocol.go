package decoder

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Request is one message from the NATS add-on: a JSON object on one line.
type Request struct {
	Subject string `json:"subject"`
	// Payload is the message payload in base64.
	Payload string `json:"payload"`
	// Headers are the NATS message headers; the decoder does not use them.
}

// DecodePayload returns the message payload.
func (r *Request) DecodePayload() ([]byte, error) {
	payload, err := base64.StdEncoding.DecodeString(r.Payload)
	if err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	return payload, nil
}

// Run serves the NATS add-on's decoder protocol until in closes: for each
// request line read from in, it writes one line to out, the decoded event or an
// empty line when the payload cannot be decoded, and flushes it. Why a payload
// could not be decoded is written to diag, one line each.
//
// Run returns nil when in closes, or the error that stopped it reading in or
// writing out.
func (d *Decoder) Run(in io.Reader, out io.Writer, diag io.Writer) error {
	r := bufio.NewReaderSize(in, 64*1024)
	w := bufio.NewWriterSize(out, 64*1024)
	var event []byte
	for {
		line, readErr := r.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) {
			// A request longer than the buffer: collect the rest of it
			long := append([]byte(nil), line...)
			for errors.Is(readErr, bufio.ErrBufferFull) {
				line, readErr = r.ReadSlice('\n')
				long = append(long, line...)
			}
			line = long
		}
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if len(line) > 0 {
			var err error
			event, err = d.handle(event[:0], line)
			if err != nil {
				// A diagnostic that cannot be written must not stop decoding
				_, _ = fmt.Fprintf(diag, "telemetry-decoder: %v\n", err)
			}
			event = append(event, '\n')
			if _, err := w.Write(event); err != nil {
				return err
			}
			if err := w.Flush(); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
	}
}

// handle decodes one request line into an event appended to dst, or returns dst
// unchanged and the reason it could not.
func (d *Decoder) handle(dst []byte, line []byte) (event []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			event, err = dst, fmt.Errorf("decoder panic: %v", r)
		}
	}()
	line = bytes.TrimRight(line, "\r\n")
	if len(bytes.TrimSpace(line)) == 0 {
		return dst, errors.New("empty request line")
	}
	var request Request
	if err := json.Unmarshal(line, &request); err != nil {
		return dst, fmt.Errorf("invalid request: %w", err)
	}
	payload, err := request.DecodePayload()
	if err == nil {
		event, err = d.Decode(dst, request.Subject, payload)
	}
	if err != nil {
		return dst, fmt.Errorf("%s: %w", request.Subject, err)
	}
	return event, nil
}
