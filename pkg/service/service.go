// Package service defines the generic Rust agent's shared state.
// Specializations embed *Service in their own Service and add
// protocol-specific fields. Mirrors service-go/pkg/service.
package service

import (
	"context"

	"github.com/codefly-dev/core/agents/services"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	"github.com/codefly-dev/core/resources"
	runners "github.com/codefly-dev/core/runners/base"
	rusthelpers "github.com/codefly-dev/core/runners/rust"
)

// Settings is the generic Rust agent's configuration. Specializations embed
// this inline (yaml:",inline") to inherit RustAgentSettings fields.
type Settings struct {
	rusthelpers.RustAgentSettings `yaml:",inline"`
}

// Service carries the shared state used by Runtime, Code, Tooling, Builder.
// Specializations embed *Service to inherit the identity, logger, location,
// and source resolution.
type Service struct {
	*services.Base
	Settings *Settings

	// SourceLocation is the path to the Rust sources, set during Load. It
	// typically points at `<service>/code` (via Settings.RustSourceDir()) but
	// falls back to the service root if there's a Cargo.toml there.
	SourceLocation string

	// ActiveEnv is the plugin's active RunnerEnvironment — set by Runtime.Init
	// via CreateRunnerEnvironment and consumed by Code / Tooling / commands so
	// every spawn routes through the same mode (native / docker / nix). Nil
	// before Runtime.Init.
	ActiveEnv runners.RunnerEnvironment
}

// New builds a generic Rust Service bound to the given agent manifest.
func New(agent *resources.Agent) *Service {
	return &Service{
		Base:     services.NewServiceBase(context.Background(), agent),
		Settings: &Settings{},
	}
}

// GetAgentInformation returns the generic Rust agent advertisement.
// Specializations should override this; their overrides typically add
// protocols (HTTP/gRPC) and techniques.
func (s *Service) GetAgentInformation(_ context.Context, _ *agentv0.AgentInformationRequest) (*agentv0.AgentInformation, error) {
	return &agentv0.AgentInformation{
		RuntimeRequirements: []*agentv0.Runtime{
			{Type: agentv0.Runtime_RUST},
			{Type: agentv0.Runtime_NIX},
		},
		Capabilities: []*agentv0.Capability{
			{Type: agentv0.Capability_BUILDER},
			{Type: agentv0.Capability_RUNTIME},
		},
		Languages: []*agentv0.Language{
			{Type: agentv0.Language_RUST},
		},
		Protocols: []*agentv0.Protocol{},
		ReadMe:    "Generic Rust service. Specializations add protocols.",
	}, nil
}
