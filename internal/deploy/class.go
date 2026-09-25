// Package deploy is the operator command for putting Hypermesh on a host.
//
// One class, one path: preflight, then configure, then connect. The first
// failure stops the path. Connect starts the existing hypermesh-wg and
// hypermesh-agent units. It does not enroll by itself and it does not
// open a second tunnel.
package deploy

import (
	"context"
	"fmt"
	"strings"
)

// Step names are the command order. Later host classes use the same names.
const (
	StepPreflight = "preflight"
	StepConfigure = "configure"
	StepConnect   = "connect"
)

// CommandOrder is the AGX path. Later classes can reuse it.
var CommandOrder = []string{StepPreflight, StepConfigure, StepConnect}

// Step is one stage of a host-class command.
type Step struct {
	Name string
	Run  func(context.Context) error
}

// Pipeline runs steps in order and stops on the first error.
type Pipeline struct {
	Steps []Step
}

// Run records each step it enters, including the one that fails.
// Later steps are not entered.
func (p Pipeline) Run(ctx context.Context, record func(name string)) error {
	for _, step := range p.Steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if record != nil {
			record(step.Name)
		}
		if step.Run == nil {
			return fmt.Errorf("%s: missing", step.Name)
		}
		if err := step.Run(ctx); err != nil {
			return fmt.Errorf("%s: %w", step.Name, err)
		}
	}
	return nil
}

// HostClass is one install path. Register another class beside AGX in Class.
type HostClass interface {
	Name() string
	Pipeline(Options) Pipeline
}

// Class returns the host class for a command name.
// agx and agx-large are the same path. The plane's class id is agx-large.
func Class(name string) (HostClass, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "agx", "agx-large":
		return AGX{}, nil
	default:
		return nil, fmt.Errorf("unknown host class %q", name)
	}
}
