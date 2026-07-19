package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
)

func TestRustFixDryRunUsesManifestEditionAndDoesNotWrite(t *testing.T) {
	if _, err := exec.LookPath("rustfmt"); err != nil {
		t.Skip("rustfmt not installed")
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
