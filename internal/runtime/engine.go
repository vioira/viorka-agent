// Package runtime defines the capability-aware proxy engine lifecycle interface.
// Implementations for sing-box, Xray, Realm are not yet included.
package runtime

import "context"

type Capabilities struct {
	ValidateConfig    bool
	HotReload         bool
	TrafficStats      bool
	MultipleInstances bool
}

type Info struct {
	Name         string
	Version      string
	Managed      bool
	Capabilities Capabilities
}

type Status struct {
	Running bool
	Healthy bool
	Detail  string
}

type Stats struct {
	RxBytes uint64
	TxBytes uint64
}

// Engine must never execute arbitrary unvalidated shell input from the Panel.
type Engine interface {
	Name() string
	Detect(context.Context) (Info, error)
	Validate(context.Context, []byte) error
	Apply(context.Context, []byte) error
	Start(context.Context) error
	Stop(context.Context) error
	Restart(context.Context) error
	Status(context.Context) (Status, error)
	Stats(context.Context) (Stats, error)
}
