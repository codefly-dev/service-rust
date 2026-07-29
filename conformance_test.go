package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agenttesting "github.com/codefly-dev/core/agents/testing"
)

// transportTerms are repository, reconciler, and delivery-mechanism tokens that
// a manifest-producing plugin must never own. They are matched case-insensitively
// as substrings, so each token is chosen to avoid colliding with legitimate Rust
// vocabulary (notably "cargo", which contains "argo").
var transportTerms = []string{
	"gitops",
	"argocd", "argoproj", "argo cd",
	"fluxcd", "flux cd",
	"reconcil",
	"repourl",
	"targetrevision",
	"appproject",
	"kubeconfig",
	"go-github",
}

// pluginOwnedKinds is the closed set of Kubernetes objects this plugin may emit.
// It contains only the workload and its configuration surface — no control-plane
// or delivery objects.
var pluginOwnedKinds = map[string]bool{
	"Namespace":  true,
	"Deployment": true,
	"Service":    true,
	"ConfigMap":  true,
	"Secret":     true,
}

// TestRuntimeSourceIsTransportNeutral asserts the plugin's runtime source carries
// no repository or reconciler integration. Test files are excluded: they are not
// shipped as runtime, and this file necessarily names the forbidden tokens.
func TestRuntimeSourceIsTransportNeutral(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		lower := strings.ToLower(string(content))
		for _, term := range transportTerms {
			if strings.Contains(lower, term) {
				t.Errorf("%s references transport term %q; a manifest producer must not own delivery or reconciler logic", name, term)
			}
		}
	}
}

// TestRenderedManifestsAreTransportNeutral renders the deployment bundle with no
// Git, network, kubeconfig, Argo, or cloud credentials available (the shared core
// harness uses none) and asserts the output is a pure manifest bundle: only
// plugin-owned workload kinds, no reconciliation control-plane objects, and no
// repository source bindings.
func TestRenderedManifestsAreTransportNeutral(t *testing.T) {
	dir := agenttesting.AssertKustomizeTemplates(t, deploymentFS, nil)

	var kinds []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if ext := filepath.Ext(path); ext != ".yaml" && ext != ".yml" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		lower := strings.ToLower(string(content))
		for _, term := range transportTerms {
			if strings.Contains(lower, term) {
				t.Errorf("%s contains transport term %q; plugin-owned manifests must carry no reconciler or repository binding", rel, term)
			}
		}
		for line := range strings.SplitSeq(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			value, ok := strings.CutPrefix(trimmed, "kind:")
			if !ok {
				continue
			}
			kind := strings.TrimSpace(value)
			kinds = append(kinds, kind)
			if !pluginOwnedKinds[kind] {
				t.Errorf("%s emits kind %q outside the plugin-owned workload set", rel, kind)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk rendered manifests: %v", err)
	}
	if len(kinds) == 0 {
		t.Fatal("no Kubernetes objects rendered; expected plugin-owned workload manifests")
	}
}
