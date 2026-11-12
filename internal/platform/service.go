// Package platform declares the service boundary. B17 supplies implementations.
package platform

import "context"

type InitSystem string

const (
	Systemd InitSystem = "systemd"
	OpenRC  InitSystem = "openrc"
)

type Role string

const (
	Controller Role = "controller"
	Node       Role = "node"
	Both       Role = "both"
)

type Component string

const (
	ControllerService Component = "meshstrata-controller"
	AgentService      Component = "meshstrata-agent"
	MihomoService     Component = "meshstrata-mihomo"
	FRPSService       Component = "meshstrata-frps"
	FRPCService       Component = "meshstrata-frpc"
)

// Instance identifies platform-owned files and service, never a generic process name.
type Instance struct {
	Component   Component
	ServiceName string
	Executable  string
	StateDir    string
	ConfigPath  string
}
type Status struct {
	Running bool
	PID     int
}

// Manager has no systemctl assumption; restart/stop only affect the selected instance.
type Manager interface {
	Start(context.Context, Instance) error
	Stop(context.Context, Instance) error
	Status(context.Context, Instance) (Status, error)
}
