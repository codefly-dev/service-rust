package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corecode "github.com/codefly-dev/core/code"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
	toolingv0 "github.com/codefly-dev/core/generated/go/codefly/services/tooling/v0"
)

func TestRustToolingProjectsRustSemanticSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lib.rs"), []byte("pub fn run() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewService()
	service.sourceLocation = dir
	response, err := corecode.NewSourceTooling(NewCode(service)).GetSemanticIndex(t.Context(), &toolingv0.GetSemanticIndexRequest{})
	if err != nil {
		t.Fatal(err)
	}
	index := response.GetIndex()
	if response.GetFailure() != nil || index.GetState() != basev0.SemanticIndexState_SEMANTIC_INDEX_STATE_COMPLETE || len(index.GetIssues()) != 0 {
		t.Fatalf("semantic response = %+v", response)
	}
	if len(index.GetLanguages()) != 1 || index.GetLanguages()[0] != "rust" || len(index.GetFiles()) != 1 || index.GetFiles()[0].GetPath() != "lib.rs" {
		t.Fatalf("Rust semantic coverage = %+v", index)
	}
	if len(index.GetSymbols()) != 1 || index.GetSymbols()[0].GetQualifiedName() != "lib.run" || !canonicalSHA256(index.GetSymbols()[0].GetDeclarationSha256()) {
		t.Fatalf("Rust semantic symbols = %+v", index.GetSymbols())
	}
}

func TestRustToolingCarriesCurrentSemanticSymbolContract(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tool.mjs"), []byte("const SCRIPT_PATH = 'tool';\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewService()
	service.sourceLocation = dir
	response, err := corecode.NewSourceTooling(NewCode(service)).GetSemanticIndex(t.Context(), &toolingv0.GetSemanticIndexRequest{})
	if err != nil {
		t.Fatal(err)
	}
	index := response.GetIndex()
	if response.GetFailure() != nil || index.GetState() != basev0.SemanticIndexState_SEMANTIC_INDEX_STATE_COMPLETE {
		t.Fatalf("semantic response = %+v", response)
	}
	for _, symbol := range index.GetSymbols() {
		if symbol.GetName() != "SCRIPT_PATH" {
			continue
		}
		hash := symbol.GetDeclarationSha256()
		if !canonicalSHA256(hash) {
			t.Fatalf("SCRIPT_PATH declaration hash = %q, want canonical SHA-256", hash)
		}
		return
	}
	t.Fatalf("SCRIPT_PATH missing from semantic symbols: %+v", index.GetSymbols())
}

func TestRustFixDryRunUsesManifestEditionAndDoesNotWrite(t *testing.T) {
	if _, err := exec.LookPath("rustfmt"); err != nil {
		t.Fatalf("rustfmt is required for the production Rust formatter proof: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname='sample'\nedition='2024'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	original := []byte("fn main(){let value=1;println!(\"{}\",value);}\n")
	if err := os.WriteFile(filepath.Join(dir, "main.rs"), original, 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewService()
	service.sourceLocation = dir
	response, err := NewCode(service).Execute(context.Background(), &codev0.CodeRequest{Operation: &codev0.CodeRequest_Fix{Fix: &codev0.FixRequest{
		File: "main.rs", Mode: basev0.FixMode_FIX_MODE_SAFE, DryRun: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	fix := response.GetFix()
	if !fix.GetSuccess() || !fix.GetChanged() || fix.GetWrote() {
		t.Fatalf("fix = %+v failure=%+v", fix, response.GetFailure())
	}
	written, err := os.ReadFile(filepath.Join(dir, "main.rs"))
	if err != nil || string(written) != string(original) {
		t.Fatalf("dry-run changed source: err=%v content=%q", err, written)
	}
}

func canonicalSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func TestRustEditionFindsNearestManifest(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "crates", "api", "src")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "crates", "api", "Cargo.toml"), []byte("[package]\nedition = \"2024\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := rustEdition(dir, "crates/api/src/lib.rs"); got != "2024" {
		t.Fatalf("edition = %q", got)
	}
}
