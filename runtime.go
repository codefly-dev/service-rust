package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/codefly-dev/core/agents/helpers/code"
	"github.com/codefly-dev/core/agents/services"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/llmout"
	"github.com/codefly-dev/core/resources"
	runners "github.com/codefly-dev/core/runners/base"
	"github.com/codefly-dev/core/wool"

	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
)

type Runtime struct {
	services.RuntimeServer

	*Service

	// Native runner environment
	runnerEnv *runners.NativeEnvironment

	// Running cargo process
	runner   runners.Proc
	testProc runners.Proc
}

func NewRuntime(service *Service) *Runtime {
	return &Runtime{Service: service}
}

func (s *Runtime) Load(ctx context.Context, req *runtimev0.LoadRequest) (*runtimev0.LoadResponse, error) {
	err := s.Base.Load(ctx, req.Identity, s.Settings)
	if err != nil {
		return s.Runtime.LoadErrorf(err, "loading base")
	}

	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	if req.DisableCatch {
		s.Wool.DisableCatch()
	}

	s.Runtime.SetEnvironment(req.Environment)

	s.sourceLocation, err = s.LocalDirCreate(ctx, "%s", s.Settings.RustSourceDir())
	if err != nil {
		return s.Runtime.LoadErrorf(err, "creating source location")
	}

	if s.Watcher != nil {
		s.Watcher.Pause()
	}

	s.Endpoints, err = s.Base.Service.LoadEndpoints(ctx)
	if err != nil {
		return s.Runtime.LoadErrorf(err, "loading endpoints")
	}

	s.RestEndpoint, err = resources.FindRestEndpoint(ctx, s.Endpoints)
	if err != nil {
		// REST endpoint is optional; log but don't fail.
		s.Wool.Debug("no REST endpoint found", wool.ErrField(err))
		s.RestEndpoint = nil
	}

	return s.Runtime.LoadResponse()
}

func (s *Runtime) Init(ctx context.Context, req *runtimev0.InitRequest) (*runtimev0.InitResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Runtime.LogInitRequest(req)

	s.Runtime.RuntimeContext = req.RuntimeContext
	s.Wool.Forwardf("starting execution environment in %s mode", s.Runtime.RuntimeContext.Kind)

	s.EnvironmentVariables.SetRuntimeContext(s.Runtime.RuntimeContext)
	s.NetworkMappings = req.ProposedNetworkMappings

	// Service's own configuration: configurations/<env>/*.env (incl. *.secret.env)
	// → the service's own configured values injected into its environment. Without
	// this a service never receives its own config (e.g. provider API keys via
	// CODEFLY__SERVICE_SECRET_CONFIGURATION__...). Mirror python-fastapi, which
	// already injects req.Configuration first.
	err := s.EnvironmentVariables.AddConfigurations(ctx, req.Configuration)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	// Project configurations
	err = s.EnvironmentVariables.AddConfigurations(ctx, req.WorkspaceConfigurations...)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	// Dependency configurations
	confs := resources.FilterConfigurations(req.DependenciesConfigurations, s.Runtime.RuntimeContext)
	s.Wool.Trace("adding configurations", wool.Field("configurations", resources.MakeManyConfigurationSummary(confs)))
	err = s.EnvironmentVariables.AddConfigurations(ctx, confs...)
	if err != nil {
		return s.Runtime.InitError(err)
	}

	// Endpoint networking: add our own endpoint addresses as env vars.
	if s.RestEndpoint != nil {
		net, err := resources.FindNetworkInstanceInNetworkMappings(ctx, s.NetworkMappings, s.RestEndpoint, resources.NewNativeNetworkAccess())
		if err != nil {
			return s.Runtime.InitError(err)
		}
		s.Infof("REST will run on %s", net.Address)

		nm, err := resources.FindNetworkMapping(ctx, s.NetworkMappings, s.RestEndpoint)
		if err != nil {
			return s.Runtime.InitError(err)
		}
		err = s.EnvironmentVariables.AddEndpoints(ctx, []*basev0.NetworkMapping{nm}, resources.NewNativeNetworkAccess())
		if err != nil {
			return s.Runtime.InitError(err)
		}
	}

	// Hot reload watcher
	if s.Settings.HotReload {
		s.Wool.Trace("starting hot reload")
		dependencies := requirements.Clone()
		dependencies.Localize(s.Location)
		s.Wool.Trace("setting up code watcher", wool.Field("dep", dependencies.All()))
		conf := services.NewWatchConfiguration(dependencies)
		err = s.SetupWatcher(ctx, conf, s.EventHandler)
		if err != nil {
			s.Wool.Warn("error in watcher", wool.ErrField(err))
		}
	}

	if s.Watcher != nil {
		s.Watcher.Resume()
	}

	// Create native runner environment
	if s.runnerEnv == nil {
		s.runnerEnv, err = runners.NewNativeEnvironment(ctx, s.sourceLocation)
		if err != nil {
			return s.Runtime.InitErrorf(err, "cannot create native environment")
		}
	}
	s.activeEnv = s.runnerEnv

	allEnvs, err := s.EnvironmentVariables.All()
	if err != nil {
		return s.Runtime.InitErrorf(err, "cannot get environment variables")
	}
	s.runnerEnv.WithEnvironmentVariables(ctx, allEnvs...)

	err = s.runnerEnv.Init(ctx)
	if err != nil {
		return s.Runtime.InitErrorf(err, "cannot init native environment")
	}

	s.Wool.Info("successful init of rust runner")

	return s.Runtime.InitResponse()
}

func (s *Runtime) Start(ctx context.Context, req *runtimev0.StartRequest) (*runtimev0.StartResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Wool.Forwardf("building and starting rust service...")

	// Stop before replacing the runner
	if s.runner != nil {
		err := s.runner.Stop(ctx)
		if err != nil {
			return s.Runtime.StartError(err)
		}
	}

	// Add dependency network mappings
	err := s.EnvironmentVariables.AddEndpoints(ctx, req.DependenciesNetworkMappings, resources.NetworkAccessFromRuntimeContext(s.Runtime.RuntimeContext))
	if err != nil {
		return s.Runtime.StartError(err)
	}

	// Add fixture
	s.EnvironmentVariables.SetFixture(req.Fixture)

	// Add per-service runtime overrides (--set <service>:KEY=VAL)
	s.EnvironmentVariables.AddOverrides(req.GetOverrides())

	startEnvs, err := s.EnvironmentVariables.All()
	if err != nil {
		return s.Runtime.StartErrorf(err, "getting environment variables")
	}

	runningContext := s.Wool.Inject(context.Background())

	var proc runners.Proc
	if s.Settings.HotReload {
		// Use cargo-watch for hot-reload
		proc, err = s.runnerEnv.NewProcess("cargo", "watch", "-x", "run")
	} else {
		// Use cargo run directly
		proc, err = s.runnerEnv.NewProcess("cargo", "run")
	}
	if err != nil {
		return s.Runtime.StartErrorf(err, "creating cargo process")
	}

	proc.WithEnvironmentVariables(ctx, startEnvs...)
	proc.WithOutput(s.Logger)

	s.runner = proc
	err = s.runner.Start(runningContext)
	if err != nil {
		if !s.Settings.HotReload {
			return s.Runtime.StartErrorf(err, "starting cargo")
		}
		s.Wool.Info("compile error, waiting for hot-reload")
		return s.Runtime.StartResponse()
	}

	s.Wool.Forwardf("rust service started and running")

	return s.Runtime.StartResponse()
}

func (s *Runtime) Build(ctx context.Context, req *runtimev0.BuildRequest) (*runtimev0.BuildResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Infof("running cargo build")

	envs, err := s.EnvironmentVariables.All()
	if err != nil {
		return s.Runtime.BuildErrorf(err, "getting environment variables")
	}

	proc, err := s.runnerEnv.NewProcess("cargo", "build")
	if err != nil {
		return s.Runtime.BuildErrorf(err, "creating cargo build process")
	}
	proc.WithEnvironmentVariables(ctx, envs...)

	var output strings.Builder
	proc.WithOutput(&output)

	err = proc.Run(ctx)
	// Compress before the output reaches the model; on failure the cargo errors
	// are the payload, and a gRPC error drops the body, so they go in the message.
	compressed := llmout.Compress("cargo", []string{"build"}, output.String())
	if err != nil {
		return s.Runtime.BuildErrorf(err, "cargo build failed:\n%s", compressed)
	}

	return s.Runtime.BuildResponse(compressed)
}

func (s *Runtime) Test(ctx context.Context, _ *runtimev0.TestRequest) (*runtimev0.TestResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Infof("running cargo test")

	testEnvs, err := s.EnvironmentVariables.All()
	if err != nil {
		return s.Runtime.TestErrorf(err, "getting environment variables")
	}

	// Stable Rust rejects libtest's JSON formatter unless the nightly-only
	// -Z unstable-options gate is enabled. Process construction cannot detect
	// that execution-time failure, so the former fallback was unreachable and
	// every stable Cargo suite was reported as failed. Plain cargo test is the
	// portable contract; structured event support must use a stable interface
	// when one exists rather than silently requiring nightly.
	proc, err := s.runnerEnv.NewProcess("cargo", "test")
	if err != nil {
		return s.Runtime.TestErrorf(err, "creating cargo test process")
	}

	proc.WithEnvironmentVariables(ctx, testEnvs...)

	var output strings.Builder
	proc.WithOutput(&output)

	s.testProc = proc
	runErr := proc.Run(ctx)
	s.testProc = nil

	// Compress the cargo test output (failures are the bulk of it) before it
	// reaches the model. On failure it carries via the error message, since a
	// gRPC error drops the response body.
	compressed := llmout.Compress("cargo", []string{"test"}, output.String())
	s.Wool.Forwardf("cargo test output:\n%s", compressed)

	if runErr != nil {
		return s.Runtime.TestResponseWithResults(0, 0, 1, 0, 0, nil, fmt.Errorf("cargo test failed:\n%s", compressed))
	}

	return s.Runtime.TestResponseWithResults(1, 1, 0, 0, 0, nil, nil)
}

func (s *Runtime) Information(ctx context.Context, req *runtimev0.InformationRequest) (*runtimev0.InformationResponse, error) {
	return s.Runtime.InformationResponse(ctx, req)
}

func (s *Runtime) Stop(ctx context.Context, _ *runtimev0.StopRequest) (*runtimev0.StopResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Wool.Trace("stopping service")
	if s.testProc != nil {
		s.Wool.Trace("stopping test process")
		_ = s.testProc.Stop(ctx)
		s.testProc = nil
	}
	if s.runner != nil {
		s.Wool.Trace("stopping runner")
		err := s.runner.Stop(ctx)
		if err != nil {
			return s.Runtime.StopError(err)
		}
		s.runner = nil
		s.Wool.Trace("runner stopped")
	}

	// Cancel the watcher and let its Start goroutine's deferred close of Events
	// run exactly once — Stop must not close Events itself, or it races that
	// goroutine into a "close of closed channel" panic.
	s.Base.StopWatcher()

	s.Wool.Trace("base stopped")
	return s.Runtime.StopResponse()
}

func (s *Runtime) Destroy(ctx context.Context, _ *runtimev0.DestroyRequest) (*runtimev0.DestroyResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	s.Wool.Trace("destroying rust service")

	// Stop the native environment if needed
	if s.runnerEnv != nil {
		_ = s.runnerEnv.Shutdown(ctx)
	}

	return s.Runtime.DestroyResponse()
}

/* Event handling for hot-reload */

func (s *Runtime) EventHandler(event code.Change) error {
	s.Wool.Info("detected change requiring re-build", wool.Field("path", event.Path))
	s.Runtime.DesiredStart()
	return nil
}
