// telemetry-decoder decodes Tesla Fleet Telemetry protobuf payloads into JSON
// events for the NATS Add-on for Splunk: it reads one request per line on stdin
// and writes one event per line on stdout until stdin closes.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/Teslemetry/telemetry-decoder/internal/decoder"
	"github.com/Teslemetry/telemetry-decoder/internal/protos"
)

// version is set at build time with -ldflags "-X main.version=..."
var version = ""

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("telemetry-decoder", flag.ContinueOnError)
	flags.Usage = func() {
		_, _ = fmt.Fprintf(flags.Output(), `Usage: telemetry-decoder [options]

Decodes Tesla Fleet Telemetry protobuf payloads for the NATS Add-on for Splunk.
Reads one JSON request per line on stdin ({"subject":..., "headers":..., "payload":<base64>})
and writes one JSON event per line on stdout, or an empty line when a payload
cannot be decoded (the reason goes to stderr). Exits when stdin closes.

Options:
`)
		flags.PrintDefaults()
	}
	format := flags.String("format", string(decoder.FormatFlat),
		`vehicle data event shape: "flat" (one field per signal) or "protojson" (data as a list of key/value pairs, as fleet-telemetry writes it)`)
	protoNames := flags.Bool("proto-names", false, "use proto field names (created_at) instead of JSON names (createdAt)")
	showVersion := flags.Bool("version", false, "print the version and exit")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "telemetry-decoder: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	if *showVersion {
		fmt.Printf("telemetry-decoder %s (tesla-protocol %s)\n", buildVersion(), protos.TeslaProtocolCommit)
		return 0
	}
	d, err := decoder.New(decoder.Options{Format: decoder.Format(*format), ProtoNames: *protoNames})
	if err != nil {
		fmt.Fprintf(os.Stderr, "telemetry-decoder: %v\n", err)
		return 2
	}
	if err := d.Run(os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "telemetry-decoder: %v\n", err)
		return 1
	}
	return 0
}

func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "dev"
}
