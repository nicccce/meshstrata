package main

import (
	"context"
	"io"
	"os"

	"github.com/nicccce/meshstrata/controller/internal/controller"
	"github.com/nicccce/meshstrata/internal/command"
	"github.com/nicccce/meshstrata/internal/config"
)

func main() {
	app := command.Component[config.Controller]{Name: "controller", Load: config.LoadController, Run: controller.Run, Actions: map[string]func(context.Context, config.Controller, io.Reader) error{"prepare-local": controller.PrepareLocal, "initialize-admin": controller.InitializeAdmin}}
	os.Exit(command.Execute(app, os.Args[1:], os.Stdout, os.Stderr))
}
