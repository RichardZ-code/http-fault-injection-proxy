// retry-client demonstrates bounded GET retries, not general retry middleware.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func run(args []string) int {
	f := flag.NewFlagSet("retry-client", flag.ContinueOnError)
	target := f.String("url", "http://127.0.0.1:8080/ok", "HTTP GET destination")
	mode := f.String("mode", "none", "none or retry")
	operations := f.Int("operations", 6, "serial logical operations (1 through 1000)")
	if f.Parse(args) != nil || f.NArg() != 0 || (*mode != "none" && *mode != "retry") || *operations < 1 || *operations > 1000 || destination(*target) != nil {
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c, tr := newClient()
	defer tr.CloseIdleConnections()
	code := 0
	for id := 1; id <= *operations; id++ {
		if ctx.Err() != nil {
			return 1
		}
		r := operate(ctx, c, *target, id, defaultPolicy(*mode))
		if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
			return 1
		}
		if r.Outcome != "success" {
			code = 1
		}
	}
	return code
}

func main() {
	code := run(os.Args[1:])
	if code == 2 {
		fmt.Fprintln(os.Stderr, "retry-client: invalid options or destination")
	}
	os.Exit(code)
}
