package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBinary runs the built decoder as the add-on does: it starts it once, sends
// a request, waits for the event, and closes stdin to stop it.
func TestBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "telemetry-decoder")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	requests, err := filepath.Glob("internal/decoder/testdata/golden/*.request")
	if err != nil || len(requests) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}

	cmd := exec.Command(bin)
	stdin, _ := cmd.StdinPipe()
	stdoutPipe, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stdout := bufio.NewReader(stdoutPipe)
	exchange := func(request string) string {
		t.Helper()
		if _, err := io.WriteString(stdin, request); err != nil {
			t.Fatal(err)
		}
		line := make(chan string, 1)
		go func() {
			s, _ := stdout.ReadString('\n')
			line <- s
		}()
		select {
		case s := <-line:
			if !strings.HasSuffix(s, "\n") {
				t.Fatalf("no complete line for %q: %q", request, s)
			}
			return strings.TrimSuffix(s, "\n")
		case <-time.After(10 * time.Second):
			t.Fatalf("no answer within 10s for %q", request)
			return ""
		}
	}
	for _, path := range requests {
		request, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		event := exchange(string(request))
		var object map[string]any
		if err := json.Unmarshal([]byte(event), &object); err != nil || object["vin"] != "TESTVIN0000000001" {
			t.Errorf("%s: event %q (%v)", path, event, err)
		}
	}
	if event := exchange(`{"subject":"telemetry.V.data","headers":{},"payload":"%%%"}` + "\n"); event != "" {
		t.Errorf("bad request answered %q, want an empty line", event)
	}
	_ = stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("exit after stdin closed: %v", err)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("decoder did not exit after stdin closed")
	}
	if !strings.Contains(stderr.String(), "telemetry.V.data: invalid payload: illegal base64") || strings.Count(stderr.String(), "\n") != 1 {
		t.Errorf("stderr %q, want one line about the bad request", stderr.String())
	}
}

func TestArguments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"-version"}, 0},
		{[]string{"-h"}, 0},
		{[]string{"-format", "xml"}, 2},
		{[]string{"-nope"}, 2},
		{[]string{"extra"}, 2},
	} {
		if got := run(tc.args); got != tc.want {
			t.Errorf("run(%q) = %d, want %d", tc.args, got, tc.want)
		}
	}
}
