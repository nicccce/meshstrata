package main

import (
	"os"

	"github.com/nicccce/meshstrata/internal/command"
	"github.com/nicccce/meshstrata/internal/config"
	"github.com/nicccce/meshstrata/controller/internal/controller"
)

func main() {
	app := command.Component[config.Controller]{Name: "controller", Load: config.LoadController, Run: controller.Run}
	os.Exit(command.Execute(app, os.Args[1:], os.Stdout, os.Stderr))
}
