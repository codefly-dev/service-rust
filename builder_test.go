package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/codefly-dev/core/agents/services"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
)

// When the caller owns the build (BuildRequest.output_directory set), Build
// renders the recipe into that directory and returns a DockerBuildPlan instead
// of running docker build in-process, so the CLI builds the image multi-arch.
func TestBuildEmitsRecipeWhenOutputDirectorySet(t *testing.T) {
	ctx := context.Background()

	location := t.TempDir()
	outputDirectory := filepath.Join(location, "builder")

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

	response, err := NewBuilder(svc).Build(ctx, request)
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
