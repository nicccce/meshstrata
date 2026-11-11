package main

import (
	"context"
	"os"

	"github.com/nicccce/meshstrata/agent/internal/agent"
	"github.com/nicccce/meshstrata/internal/command"
	"github.com/nicccce/meshstrata/internal/config"
)

func main() {
	app := command.Component[config.Agent]{
		Name: "agent", Load: config.LoadAgent, Run: agent.Run,
		Healthcheck: func(ctx context.Context, cfg config.Agent) (any, error) { return agent.CheckHealth(ctx, cfg) },
	}
	os.Exit(command.Execute(app, os.Args[1:], os.Stdout, os.Stderr))
}
