package main

import (
	"context"
	"embed"

	"github.com/codefly-dev/core/builders"
	"github.com/codefly-dev/core/templates"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/codefly-dev/core/agents"
	"github.com/codefly-dev/core/agents/services"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	"github.com/codefly-dev/core/languages"
	configurations "github.com/codefly-dev/core/resources"
	runnersbase "github.com/codefly-dev/core/runners/base"
	"github.com/codefly-dev/core/shared"
)

// Agent version
var agent = shared.Must(configurations.LoadFromFs[configurations.Agent](shared.Embed(infoFS)))

var requirements = builders.NewDependencies(agent.Name,
	builders.NewDependency("service.codefly.yaml"),
	builders.NewDependency("code").WithPathSelect(shared.NewSelect("*.rs")),
)

// Settings holds agent-specific configuration stored in service.codefly.yaml.
type Settings struct {
	HotReload     bool   `yaml:"hot-reload"`
	SourceDir     string `yaml:"source-dir"`
	WithWorkspace bool   `yaml:"with-workspace"`
	// OpenAPI opts the service into REST-endpoint generation: when set, Sync runs
	// the binary's `openapi` subcommand to emit openapi/api.swagger.json, which
	// CreateEndpoints then loads to materialize the REST endpoint. Off by default —
	// it requires the project to implement that subcommand (e.g. via utoipa).
	OpenAPI bool `yaml:"openapi"`
	// GrpcClientDir is where generated per-dependency gRPC clients land (relative
	// to the service root). Defaults to "code/src/external". A cargo workspace
	// should point this at the crate that consumes the client (e.g.
	// "code/crates/<engine>/src/external") so the engine crate owns the client
	// rather than the thin bin crate.
	GrpcClientDir string `yaml:"grpc-client-dir"`
}

// RustSourceDir returns the configured source directory, defaulting to "code".
func (s *Settings) RustSourceDir() string {
	if s.SourceDir != "" {
		return s.SourceDir
	}
	return "code"
}

// GrpcClientOut returns the dir for generated gRPC clients, defaulting to
// "code/src/external" (the bin crate's src).
func (s *Settings) GrpcClientOut() string {
	if s.GrpcClientDir != "" {
		return s.GrpcClientDir
	}
	return "code/src/external"
}

const SettingHotReload = "hot-reload"
const SettingWithWorkspace = "with-workspace"
const SettingOpenAPI = "openapi"

// Service holds shared state between Runtime and Builder.
type Service struct {
	*services.Base

	// Endpoints
	RestEndpoint *basev0.Endpoint

	// Settings
	*Settings

	sourceLocation string
}

func (s *Service) GetAgentInformation(ctx context.Context, _ *agentv0.AgentInformationRequest) (*agentv0.AgentInformation, error) {
	defer s.Wool.Catch()

	info := s.Information
	if info == nil {
		info = &services.Information{}
	}
	readme, err := templates.ApplyTemplateFrom(ctx, shared.Embed(readmeFS), "templates/agent/README.md", info)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return services.Advertisement{
		Backends: runnersbase.BackendSupport{
			Local:  func() bool { return languages.HasCargoRuntime(nil) },
			Nix:    true,
			Docker: true,
		},
		Toolchains: []agentv0.Toolchain_Type{agentv0.Toolchain_RUST, agentv0.Toolchain_CARGO},
		Protocols:  []agentv0.Protocol_Type{agentv0.Protocol_HTTP},
		ReadMe:     readme,
	}.Build(), nil
}

func NewService() *Service {
	return &Service{
		Base:     services.NewServiceBase(context.Background(), agent),
		Settings: &Settings{},
	}
}

func main() {
	svc := NewService()
	agents.Serve(agents.PluginRegistration{
		Agent:   svc,
		Runtime: NewRuntime(),
		Builder: NewBuilder(),
	})
}

//go:embed agent.codefly.yaml
var infoFS embed.FS

//go:embed templates/agent
var readmeFS embed.FS
