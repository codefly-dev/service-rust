package main

import (
	"context"
	"embed"
	"fmt"

	"github.com/codefly-dev/core/agents/communicate"
	dockerhelpers "github.com/codefly-dev/core/agents/helpers/docker"
	"github.com/codefly-dev/core/agents/services"
	"github.com/codefly-dev/core/companions/proto"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/codefly-dev/core/languages"
	"github.com/codefly-dev/core/resources"
	runners "github.com/codefly-dev/core/runners/base"
	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/standards"
	"github.com/codefly-dev/core/templates"
	"github.com/codefly-dev/core/wool"
)

type Builder struct {
	services.BuilderServer

	*Service

	// Answers from interactive Communicate stream
	answers map[string]*agentv0.Answer
}

func NewBuilder() *Builder {
	return &Builder{
		Service: NewService(),
	}
}

func (s *Builder) Load(ctx context.Context, req *builderv0.LoadRequest) (*builderv0.LoadResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	err := s.Builder.Load(ctx, req.Identity, s.Settings)
	if err != nil {
		return s.Builder.LoadError(err)
	}

	s.sourceLocation = s.Local("%s", s.Settings.RustSourceDir())

	requirements.Localize(s.Location)

	if req.CreationMode != nil {
		s.Builder.CreationMode = req.CreationMode

		s.Builder.GettingStarted, err = templates.ApplyTemplateFrom(ctx, shared.Embed(factoryFS), "templates/factory/GETTING_STARTED.md", s.Information)
		if err != nil {
			return s.Builder.LoadError(err)
		}

		return s.Builder.LoadResponse()
	}

	s.Endpoints, err = s.Base.Service.LoadEndpoints(ctx)
	if err != nil {
		return s.Builder.LoadError(err)
	}

	s.RestEndpoint, err = resources.FindRestEndpoint(ctx, s.Endpoints)
	if err != nil {
		s.Wool.Debug("no REST endpoint found", wool.ErrField(err))
		s.RestEndpoint = nil
	}

	return s.Builder.LoadResponse()
}

func (s *Builder) Init(ctx context.Context, req *builderv0.InitRequest) (*builderv0.InitResponse, error) {
	defer s.Wool.Catch()

	s.Builder.LogInitRequest(req)

	ctx = s.Wool.Inject(ctx)

	s.DependencyEndpoints = req.DependenciesEndpoints

	return s.Builder.InitResponse()
}

func (s *Builder) Update(ctx context.Context, _ *builderv0.UpdateRequest) (*builderv0.UpdateResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	return &builderv0.UpdateResponse{}, nil
}

// Audit returns Tool="missing" until cargo audit integration lands —
// rust agent is WIP per agents/CLAUDE.md. The empty-but-valid response
// lets `codefly audit workspace` aggregate without erroring on rust.
func (s *Builder) Audit(ctx context.Context, _ *builderv0.AuditRequest) (*builderv0.AuditResponse, error) {
	defer s.Wool.Catch()
	return s.Builder.AuditResponse(nil, nil, "missing", "RUST")
}

// Upgrade is a NOOP for rust until cargo update integration lands.
func (s *Builder) Upgrade(ctx context.Context, _ *builderv0.UpgradeRequest) (*builderv0.UpgradeResponse, error) {
	defer s.Wool.Catch()
	return s.Builder.UpgradeResponse(nil, "")
}

// Sync generates a gRPC client (prost + tonic) for every declared
// dependency that exposes a gRPC endpoint. The Rust service itself does
// not own protos, so there is no local proto generation step (unlike the
// go-grpc agent) — only dependency clients are emitted, under
// code/src/external/<dep>.
func (s *Builder) Sync(ctx context.Context, _ *builderv0.SyncRequest) (*builderv0.SyncResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Wool.Debug("dependencies", wool.Field("dependencies", s.Base.Service.ServiceDependencies))
	for _, dep := range s.Base.Service.ServiceDependencies {
		ep, err := resources.FindGRPCEndpointFromService(ctx, dep, s.DependencyEndpoints)
		if err != nil {
			return s.Builder.SyncError(err)
		}
		if ep == nil {
			continue
		}
		// A dependency may override where ITS client lands (e.g. into a plugin
		// crate that owns it), otherwise it goes under the service-level dir.
		dir := dep.GrpcClientDir
		if dir == "" {
			dir = s.Settings.GrpcClientOut()
		}
		dest := s.Local("%s/%s", dir, dep.Unique())
		if err := proto.GenerateGRPC(ctx, languages.RUST, dest, dep.Unique(), ep); err != nil {
			return s.Builder.SyncError(err)
		}
	}

	// Opt-in: regenerate the OpenAPI spec from the service's own `openapi`
	// subcommand so the REST endpoint stays in sync with the code.
	if s.Settings.OpenAPI {
		if err := s.GenerateOpenAPI(ctx); err != nil {
			return s.Builder.SyncError(err)
		}
	}
	return s.Builder.SyncResponse()
}

// GenerateOpenAPI runs the service binary's `openapi <out>` subcommand to emit
// the spec at standards.OpenAPIPath, which CreateEndpoints then loads to
// materialize the REST endpoint. The project must implement that subcommand
// (e.g. with utoipa). This is the Rust analogue of the python-fastapi agent's
// src/openapi.py step — but at Sync time, since the Rust spec is pure codegen
// (`warden openapi` builds the document from compile-time annotations, no
// running server required).
func (s *Builder) GenerateOpenAPI(ctx context.Context) error {
	env, err := runners.NewNativeEnvironment(ctx, s.sourceLocation)
	if err != nil {
		return s.Wool.Wrapf(err, "cannot create native environment")
	}
	defer func() {
		if shutErr := env.Shutdown(ctx); shutErr != nil {
			s.Wool.Warn("cannot shutdown openapi runner", wool.ErrField(shutErr))
		}
	}()
	if err := env.Init(ctx); err != nil {
		return s.Wool.Wrapf(err, "cannot init native environment")
	}
	out := s.Local(standards.OpenAPIPath)
	proc, err := env.NewProcess("cargo", "run", "--quiet", "--", "openapi", out)
	if err != nil {
		return s.Wool.Wrapf(err, "cannot create openapi process")
	}
	if err := proc.Run(ctx); err != nil {
		return s.Wool.Wrapf(err, "cannot generate openapi spec")
	}
	s.Wool.Info("generated openapi spec", wool.Field("path", out))
	return nil
}

// DockerTemplating holds data passed to Dockerfile.tmpl.
type DockerTemplating struct {
	Envs []resources.EnvironmentVariable
}

func (s *Builder) Build(ctx context.Context, req *builderv0.BuildRequest) (*builderv0.BuildResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Infof("building rust service docker image")

	dockerRequest, err := s.Builder.DockerBuildRequest(ctx, req)
	if err != nil {
		return s.Builder.BuildError(err)
	}

	image := s.Base.DockerImage(dockerRequest)
	s.Wool.Debug("building docker image", wool.Field("image", image.FullName()))

	if !dockerhelpers.IsValidDockerImageName(image.Name) {
		return s.Builder.BuildError(fmt.Errorf("invalid docker image name: %s", image.Name))
	}

	docker := DockerTemplating{}

	_ = shared.DeleteFile(ctx, s.Location+"/builder/Dockerfile")

	err = s.Templates(ctx, docker, services.WithBuilder(builderFS))
	if err != nil {
		return s.Builder.BuildError(err)
	}

	b, err := dockerhelpers.NewBuilder(dockerhelpers.BuilderConfiguration{
		Root:        s.Location,
		Dockerfile:  "builder/Dockerfile",
		Ignorefile:  "builder/dockerignore",
		Destination: image,
		Output:      s.Wool,
	})
	if err != nil {
		return s.Builder.BuildError(err)
	}

	_, err = b.Build(ctx)
	if err != nil {
		return s.Builder.BuildError(err)
	}

	s.Builder.WithDockerImages(image)
	return s.Builder.BuildResponse()
}

func (s *Builder) Deploy(ctx context.Context, req *builderv0.DeploymentRequest) (*builderv0.DeploymentResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	return s.Builder.DeployKustomize(ctx, req, services.KustomizeDeployment{
		EnvironmentVariables: s.EnvironmentVariables,
		Templates:            deploymentFS,
		Inputs:               services.ApplicationDeploymentInputs(),
	})
}

func (s *Builder) CreateEndpoints(ctx context.Context) error {
	rest, err := resources.LoadRestAPI(ctx, shared.Pointer(s.Local(standards.OpenAPIPath)))
	if err != nil {
		// No openapi yet; create a basic REST endpoint.
		endpoint := s.Base.BaseEndpoint(standards.REST)
		s.RestEndpoint, err = resources.NewAPI(ctx, endpoint, resources.ToRestAPI(nil))
		if err != nil {
			return s.Wool.Wrapf(err, "cannot create rest endpoint")
		}
		s.Endpoints = append(s.Endpoints, s.RestEndpoint)
		return nil
	}
	endpoint := s.Base.BaseEndpoint(standards.REST)
	s.RestEndpoint, err = resources.NewAPI(ctx, endpoint, resources.ToRestAPI(rest))
	if err != nil {
		return s.Wool.Wrapf(err, "cannot create rest endpoint")
	}
	s.Endpoints = append(s.Endpoints, s.RestEndpoint)
	return nil
}

func (s *Builder) Options() []*agentv0.Question {
	return []*agentv0.Question{
		communicate.NewConfirm(&agentv0.Message{Name: SettingHotReload, Message: "Code hot-reload (Recommended)?", Description: "codefly can restart your service when code changes are detected"}, true),
		communicate.NewConfirm(&agentv0.Message{Name: SettingWithWorkspace, Message: "Use cargo workspace?", Description: "Organize Rust code as a cargo workspace"}, false),
		communicate.NewConfirm(&agentv0.Message{Name: SettingOpenAPI, Message: "Generate REST endpoint from OpenAPI?", Description: "Run the binary's `openapi` subcommand on sync to emit openapi/api.swagger.json"}, false),
	}
}

type CreateConfiguration struct {
	*services.Information
	Envs []string
}

func (s *Builder) Create(ctx context.Context, _ *builderv0.CreateRequest) (*builderv0.CreateResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	if s.Builder.CreationMode != nil && s.Builder.CreationMode.Communicate && s.answers != nil {
		var err error
		s.Settings.HotReload, err = communicate.Confirm(s.answers, SettingHotReload)
		if err != nil {
			return s.Builder.CreateError(err)
		}
		s.Settings.WithWorkspace, err = communicate.Confirm(s.answers, SettingWithWorkspace)
		if err != nil {
			return s.Builder.CreateError(err)
		}
		s.Settings.OpenAPI, err = communicate.Confirm(s.answers, SettingOpenAPI)
		if err != nil {
			return s.Builder.CreateError(err)
		}
	} else {
		options := s.Options()
		var err error
		s.Settings.HotReload, err = communicate.GetDefaultConfirm(options, SettingHotReload)
		if err != nil {
			return s.Builder.CreateError(err)
		}
		s.Settings.WithWorkspace, err = communicate.GetDefaultConfirm(options, SettingWithWorkspace)
		if err != nil {
			return s.Builder.CreateError(err)
		}
		s.Settings.OpenAPI, err = communicate.GetDefaultConfirm(options, SettingOpenAPI)
		if err != nil {
			return s.Builder.CreateError(err)
		}
	}

	create := CreateConfiguration{
		Information: s.Information,
		Envs:        []string{},
	}
	ignore := shared.NewIgnore("target", "Cargo.lock")

	err := s.Templates(ctx, create, services.WithFactory(factoryFS).WithPathSelect(ignore))
	if err != nil {
		return s.Builder.CreateError(err)
	}

	err = s.CreateEndpoints(ctx)
	if err != nil {
		return nil, s.Wool.Wrapf(err, "cannot create endpoints")
	}

	return s.Builder.CreateResponse(ctx, s.Settings)
}

func (s *Builder) Communicate(stream builderv0.Builder_CommunicateServer) error {
	asker := communicate.NewQuestionAsker(stream)
	answers, err := asker.RunSequence(s.Options())
	if err != nil {
		return err
	}
	s.answers = answers
	return nil
}

//go:embed templates/factory
var factoryFS embed.FS

//go:embed templates/builder
var builderFS embed.FS

//go:embed templates/deployment
var deploymentFS embed.FS
