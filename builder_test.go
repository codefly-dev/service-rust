package main

import (
	"context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codefly-dev/core/agents/services"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
)

func TestBuildEmitsRecipeWhenOutputDirectorySet(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, executable := range []string{"docker", "buildx"} {
		if _, err := exec.LookPath(executable); err == nil {
			t.Fatalf("%s unexpectedly on PATH", executable)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	location := t.TempDir()
	outputDirectory := filepath.Join(t.TempDir(), "recipes")

	protected := filepath.Join(location, "protected")
	if err := os.WriteFile(protected, []byte("must stay unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outputDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(protected, filepath.Join(outputDirectory, "Dockerfile")); err != nil {
		t.Fatal(err)
	}

	svc := NewService()
	identity := &basev0.ServiceIdentity{
		Workspace:     "test",
		WorkspacePath: location,
		Module:        "test-module",
		Name:          "test-service",
		Version:       agent.Version,
	}
	if err := svc.HeadlessLoad(ctx, identity); err != nil {
		t.Fatalf("headless load: %v", err)
	}

	request := &builderv0.BuildRequest{
		OutputDirectory: outputDirectory,
		BuildContext: &builderv0.BuildContext{Kind: &builderv0.BuildContext_DockerBuildContext{
			DockerBuildContext: &builderv0.DockerBuildContext{DockerRepository: "registry.example.com"},
		}},
	}

	server := grpc.NewServer()
	builderv0.RegisterBuilderServer(server, NewBuilder(svc))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})
	client := services.NewBuilderAgentClient(connection)
	for _, selection := range []string{"", "explicit", "cache-selected"} {
		request.GetBuildContext().GetDockerBuildContext().BuildxBuilder = selection
		if selection == "cache-selected" {
			request.GetBuildContext().GetDockerBuildContext().Cache = &builderv0.BuildCacheOptions{Backend: "registry", Scope: "test/app", Exports: []string{"registry.example.com/cache"}}
		}
		response, err := client.Build(ctx, request)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if state := response.GetState().GetState(); state != builderv0.BuildStatus_SUCCESS {
			t.Fatalf("build state = %v: %s", state, response.GetState().GetMessage())
		}

		plan := response.GetResult().GetDockerBuildPlan()
		if plan == nil {
			t.Fatalf("expected a docker build plan, got %T", response.GetResult().GetKind())
		}
		if plan.GetContractVersion() != services.DockerBuildRecipeContractVersion {
			t.Fatalf("contract version = %q, want %q", plan.GetContractVersion(), services.DockerBuildRecipeContractVersion)
		}
		if len(plan.GetRecipes()) != 1 {
			t.Fatalf("expected 1 recipe, got %d", len(plan.GetRecipes()))
		}
		recipe := plan.GetRecipes()[0]
		if recipe.GetDockerfile() != "Dockerfile" || recipe.GetContext() != "." {
			t.Fatalf("recipe references dockerfile %q context %q", recipe.GetDockerfile(), recipe.GetContext())
		}
		if recipe.GetImage() == "" {
			t.Fatalf("recipe carries no image reference")
		}
		if got := recipe.GetPlatforms(); len(got) != 2 || got[0] != "linux/amd64" || got[1] != "linux/arm64" {
			t.Fatalf("recipe platforms = %v, want [linux/amd64 linux/arm64]", got)
		}

		// The plan is emitted over the on-disk recipe tree, so the caller can verify
		// the tree it received against the plan's digest before building.
		if err := services.VerifyDockerBuildPlan(outputDirectory, plan); err != nil {
			t.Fatalf("verify plan against emitted tree: %v", err)
		}
		if _, err := os.Stat(filepath.Join(outputDirectory, "Dockerfile")); err != nil {
			t.Fatalf("Dockerfile not rendered into output directory: %v", err)
		}
	}
	untouched, err := os.ReadFile(protected)
	if err != nil {
		t.Fatal(err)
	}
	if string(untouched) != "must stay unchanged" {
		t.Fatal("rendering overwrote the Dockerfile symlink target")
	}
	fresh, err := os.ReadFile(filepath.Join(outputDirectory, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{"", "relative"} {
		request.OutputDirectory = destination
		response, err := client.Build(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if response.GetState().GetState() != builderv0.BuildStatus_ERROR || !strings.Contains(response.GetState().GetMessage(), "output_directory") {
			t.Fatalf("expected destination rejection: %v", response)
		}
		unchanged, err := os.ReadFile(filepath.Join(outputDirectory, "Dockerfile"))
		if err != nil {
			t.Fatal(err)
		}
		if string(unchanged) != string(fresh) {
			t.Fatal("rejected build modified recipe")
		}
		if _, err := os.Stat(filepath.Join(location, "builder")); !os.IsNotExist(err) {
			t.Fatalf("rejected build prepared service builder directory: %v", err)
		}
	}
}
