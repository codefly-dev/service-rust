// Binary service-rust is the generic Rust agent entry point.
//
// All logic lives under ./pkg so specializations can import and compose the
// reusable pieces:
//
//	github.com/codefly-dev/service-rust/pkg/service   — shared Service/Settings
//	github.com/codefly-dev/service-rust/pkg/runtime   — Runtime gRPC server
//	github.com/codefly-dev/service-rust/pkg/code      — Code gRPC server
//	github.com/codefly-dev/service-rust/pkg/tooling   — Tooling gRPC server
//	github.com/codefly-dev/service-rust/pkg/builder   — Builder gRPC server
//
// Templates are embedded here (at the binary root) and passed to
// pkg/builder — //go:embed cannot reach up from a subpackage.
package main

import (
	"embed"

	"github.com/codefly-dev/core/agents"
	"github.com/codefly-dev/core/builders"
	"github.com/codefly-dev/core/resources"
	"github.com/codefly-dev/core/shared"

	rustbuilder "github.com/codefly-dev/service-rust/pkg/builder"
	rustcode "github.com/codefly-dev/service-rust/pkg/code"
	rustruntime "github.com/codefly-dev/service-rust/pkg/runtime"
	rustservice "github.com/codefly-dev/service-rust/pkg/service"
	rusttooling "github.com/codefly-dev/service-rust/pkg/tooling"
)

// Agent version loaded from agent.codefly.yaml.
var agent = shared.Must(resources.LoadFromFs[resources.Agent](shared.Embed(infoFS)))

// File dependencies watched for change detection during build.
var requirements = builders.NewDependencies(agent.Name,
	builders.NewDependency("service.codefly.yaml"),
	builders.NewDependency("code").WithPathSelect(shared.NewSelect("*.rs")),
)

// Rust and Alpine versions used by the default container build.
const (
	RustVersion   = "1.83"
	AlpineVersion = "3.21"
)

func main() {
	svc := rustservice.New(agent)
	code := rustcode.New(svc)
	rt := rustruntime.New(svc)
	agents.Serve(agents.PluginRegistration{
		Agent:   svc,
		Runtime: rt,
		Builder: rustbuilder.New(svc, rustbuilder.BuildConfig{
			FactoryFS:     factoryFS,
			BuilderFS:     builderFS,
			DeploymentFS:  deploymentFS,
			Requirements:  requirements,
			RustVersion:   RustVersion,
			AlpineVersion: AlpineVersion,
		}),
		Code:    code,
		Tooling: rusttooling.New(code, rt),
	})
}

//go:embed agent.codefly.yaml
var infoFS embed.FS

//go:embed templates/factory
var factoryFS embed.FS

//go:embed templates/builder
var builderFS embed.FS

//go:embed templates/deployment
var deploymentFS embed.FS
