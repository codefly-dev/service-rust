package main

import (
	"context"
	"os"
	"testing"

	"github.com/codefly-dev/core/agents/services"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/codefly-dev/core/resources"
)

// guardImageDigest pins the workload image so restricted rendering satisfies the
// contract's digest-pinning requirement without a real build.
const guardImageDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// TestManifestGuardRender is the render entry point the reusable
// codefly-dev/.github manifest-guard workflow drives. It renders the plugin's
// deployment bundle through the production Deploy path into the destination the
// guard supplies, using the guard's environment, namespace, and output profile.
// With CODEFLY_MANIFEST_DESTINATION unset it skips, so ordinary `go test ./...`
// stays usable. The guard invokes it twice and requires byte-identical trees.
func TestManifestGuardRender(t *testing.T) {
	destination := os.Getenv("CODEFLY_MANIFEST_DESTINATION")
	if destination == "" {
		t.Skip("CODEFLY_MANIFEST_DESTINATION unset")
	}
	environment := envOrDefault("CODEFLY_MANIFEST_ENVIRONMENT", "manifest-guard")
	namespace := envOrDefault("CODEFLY_MANIFEST_NAMESPACE", "codefly-manifest-guard")

	profileName := os.Getenv("CODEFLY_MANIFEST_PROFILE")
	if profileName == "" {
		profileName = builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_RESTRICTED_PORTABLE_V1.String()
	}
	profileValue, ok := builderv0.KubernetesOutputProfile_value[profileName]
	if !ok {
		t.Fatalf("unknown CODEFLY_MANIFEST_PROFILE %q", profileName)
	}

	ctx := context.Background()
	svc := NewService()
	identity := &basev0.ServiceIdentity{
		Workspace: "manifest-guard",
		Module:    "manifest-guard",
		Name:      agent.Name,
		Version:   agent.Version,
	}
	if err := svc.HeadlessLoad(ctx, identity); err != nil {
		t.Fatalf("headless load: %v", err)
	}
	svc.Information = &services.Information{
		Service: resources.ToServiceWithCase(svc.Identity),
		Module:  resources.ToModuleWithCase(svc.Identity),
		Agent:   svc.Agent,
	}
	svc.EnvironmentVariables.SetIdentity(identity)
	svc.SetDockerImage(&resources.DockerImage{Name: "codefly/" + agent.Name, Digest: guardImageDigest})

	request := &builderv0.DeploymentRequest{
		Environment: &basev0.Environment{Name: environment},
		Deployment: &builderv0.Deployment{Kind: &builderv0.Deployment_Kubernetes{
			Kubernetes: &builderv0.KubernetesDeployment{
				Namespace:   namespace,
				Destination: destination,
				Profile:     builderv0.KubernetesOutputProfile(profileValue),
			},
		}},
	}

	response, err := NewBuilder(svc).Deploy(ctx, request)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if state := response.GetState().GetState(); state != builderv0.DeploymentStatus_SUCCESS {
		t.Fatalf("deploy state = %v: %s", state, response.GetState().GetMessage())
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
