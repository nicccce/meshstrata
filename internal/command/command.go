package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/nicccce/meshstrata/internal/buildinfo"
	"github.com/nicccce/meshstrata/internal/logging"
)

type Configuration interface {
	LoggingLevel() string
}

// Component keeps shared CLI behavior independent of either runtime. In
// particular, Agent must not link Controller's SQLite driver or web assets.
type Component[C Configuration] struct {
	Name        string
	Load        func(string) (C, error)
	Run         func(context.Context, C, *slog.Logger) error
	Healthcheck func(context.Context, C) (any, error)
	Actions     map[string]func(context.Context, C, io.Reader) error
}

func Execute[C Configuration](app Component[C], args []string, stdout, stderr io.Writer) int {
	component := app.Name
	logger := logging.New(stderr, "warn")
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet("meshstrata-"+component+" "+cmd, flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "", "JSON configuration file (relative paths use the working directory)")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: meshstrata-%s [run|check-config|version", component)
		for name := range app.Actions {
			fmt.Fprintf(stderr, "|%s", name)
		}
		if app.Healthcheck != nil {
			fmt.Fprint(stderr, "|healthcheck")
		}
		fmt.Fprintln(stderr, "] [-config path]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		logger.Error("Unexpected command arguments")
		return 2
	}
	if cmd == "version" {
		return encode(stdout, stderr, buildinfo.Current(component))
	}
	if cmd != "run" && cmd != "check-config" && !(app.Healthcheck != nil && cmd == "healthcheck") && app.Actions[cmd] == nil {
		logger.Error("Unknown command", "command", cmd)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := app.Load(*path)
	if err != nil {
		logger.Error("Invalid configuration", "error", err)
		return 2
	}
	if action := app.Actions[cmd]; action != nil {
		if err := action(ctx, cfg, os.Stdin); err != nil {
			logger.Error("Local action failed", "error", err)
			return 1
		}
		return encode(stdout, stderr, map[string]string{"status": "complete", "action": cmd})
	}
	if cmd == "check-config" {
		return encode(stdout, stderr, map[string]string{"status": "valid", "component": component})
	}
	if cmd == "healthcheck" {
		health, err := app.Healthcheck(ctx, cfg)
		if err != nil {
			logger.Error("Health check failed", "component", component, "error", err)
			return 1
		}
		return encode(stdout, stderr, health)
	}
	if err := app.Run(ctx, cfg, logging.New(stderr, cfg.LoggingLevel())); err != nil {
		logger.Error("Component failed", "component", component, "error", err)
		return 1
	}
	return 0
}

func encode(stdout, stderr io.Writer, value any) int {
	if err := json.NewEncoder(stdout).Encode(value); err != nil {
		logging.New(stderr, "warn").Error("Write command output failed", "error", err)
		return 1
	}
	return 0
}
