// Package code implements the generic Rust Code gRPC service.
//
// Note on code intelligence: core has no RustCodeServer (no AST/LSP backend
// for Rust yet), so this builds on the generic *corecode.DefaultCodeServer —
// which provides file / git / search / apply_edit ops — and adds Rust-specific
// overrides:
//   - fix: rustfmt
//   - add_dependency / remove_dependency: cargo add / cargo remove
//
// Symbol extraction (ListSymbols) and call-graph analysis are not yet
// implemented for Rust; they should be restored once a core Rust LSP /
// rust-analyzer client exists. Mirrors service-go/pkg/code.
package code

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	corecode "github.com/codefly-dev/core/code"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
	runners "github.com/codefly-dev/core/runners/base"
	"github.com/codefly-dev/core/wool"

	rustservice "github.com/codefly-dev/service-rust/pkg/service"
)

// runTool wraps a short-lived toolchain command in the plugin's active
// RunnerEnvironment (native / docker / nix) and returns captured output.
// When ActiveEnv is nil (Runtime.Init hasn't run), resolves a standalone env
// from the plugin's declared RuntimeContext. Mirrors service-go/pkg/code.
func (c *Code) runTool(ctx context.Context, dir, cmd string, args ...string) ([]byte, error) {
	env := c.Service.ActiveEnv
	if env == nil {
		var rctx *basev0.RuntimeContext
		if c.Service.Base != nil && c.Service.Base.Runtime != nil {
			rctx = c.Service.Base.Runtime.RuntimeContext
		}
		env = runners.ResolveStandaloneEnvironment(ctx, dir, rctx)
	}
	proc, err := env.NewProcess(cmd, args...)
	if err != nil {
		return nil, err
	}
	proc.WithDir(dir)
	var buf bytes.Buffer
	proc.WithOutput(&buf)
	runErr := proc.Run(ctx)
	return buf.Bytes(), runErr
}

// Code is the generic Rust Code server. It embeds DefaultCodeServer from core
// (file ops, git, search, apply_edit) and adds Rust-specific handlers via
// Override — rustfmt Fix and cargo add / cargo remove deps.
type Code struct {
	*corecode.DefaultCodeServer
	Service *rustservice.Service

	initialized bool
}

// New builds a generic Rust Code server bound to the shared Service.
func New(svc *rustservice.Service) *Code {
	return &Code{
		Service:           svc,
		DefaultCodeServer: corecode.NewDefaultCodeServer("."),
	}
}

// InitServer creates the DefaultCodeServer once SourceDir is resolved.
func (c *Code) InitServer() {
	c.DefaultCodeServer = corecode.NewDefaultCodeServer(c.SourceDir())
	c.registerOverrides()
	c.initialized = true
}

// EnsureInit lazily swaps in a DefaultCodeServer pointed at the resolved
// source directory the first time an RPC lands.
func (c *Code) EnsureInit() {
	if !c.initialized {
		c.InitServer()
	}
}

// SourceDir returns the directory to operate on. Resolution:
// Service.SourceLocation → $CODEFLY_AGENT_WORKDIR → <Location>/code.
func (c *Code) SourceDir() string {
	if c.Service.SourceLocation != "" {
		return c.Service.SourceLocation
	}
	if wd := os.Getenv("CODEFLY_AGENT_WORKDIR"); wd != "" {
		return wd
	}
	return c.Service.Location + "/code"
}

// registerOverrides wires Rust-specific handlers on top of DefaultCodeServer.
func (c *Code) registerOverrides() {
	c.Override("fix", c.handleFix)
	c.Override("add_dependency", c.handleAddDependency)
	c.Override("remove_dependency", c.handleRemoveDependency)
}

// Execute lazily initializes then dispatches to the embedded server.
func (c *Code) Execute(ctx context.Context, req *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	c.EnsureInit()
	return c.DefaultCodeServer.Execute(ctx, req)
}

// --- Rust-specific: Fix (rustfmt) ---

func (c *Code) handleFix(ctx context.Context, req *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	r := req.GetFix()
	absPath := filepath.Join(c.SourceDir(), r.File)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return fixResp(false, "", fmt.Sprintf("file not found: %s", r.File), nil), nil
	}

	tmpFile, err := os.CreateTemp("", "mind-fix-*.rs")
	if err != nil {
		return fixResp(false, "", fmt.Sprintf("create temp: %v", err), nil), nil
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fixResp(false, "", fmt.Sprintf("write temp: %v", err), nil), nil
	}
	_ = tmpFile.Close()

	tmpDir := filepath.Dir(tmpPath)
	var actions []string
	if out, err := c.runTool(ctx, tmpDir, "rustfmt", tmpPath); err != nil {
		wool.Get(ctx).In("Code.Fix").Warn("rustfmt failed", wool.Field("error", string(out)))
	} else {
		actions = append(actions, "rustfmt")
	}
	result, _ := os.ReadFile(tmpPath)
	return fixResp(true, string(result), "", actions), nil
}

// --- Rust-specific: Dependency management (cargo add / cargo remove) ---

func (c *Code) handleAddDependency(ctx context.Context, req *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	r := req.GetAddDependency()
	pkg := r.PackageName
	if r.Version != "" {
		pkg += "@" + r.Version
	}
	out, err := c.runTool(ctx, c.SourceDir(), "cargo", "add", pkg)
	if err != nil {
		return &codev0.CodeResponse{Result: &codev0.CodeResponse_AddDependency{AddDependency: &codev0.AddDependencyResponse{
			Success: false, Error: fmt.Sprintf("cargo add: %s", string(out)),
		}}}, nil
	}
	return &codev0.CodeResponse{Result: &codev0.CodeResponse_AddDependency{AddDependency: &codev0.AddDependencyResponse{Success: true, InstalledVersion: r.Version}}}, nil
}

func (c *Code) handleRemoveDependency(ctx context.Context, req *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	r := req.GetRemoveDependency()
	if out, err := c.runTool(ctx, c.SourceDir(), "cargo", "remove", r.PackageName); err != nil {
		return &codev0.CodeResponse{Result: &codev0.CodeResponse_RemoveDependency{RemoveDependency: &codev0.RemoveDependencyResponse{
			Success: false, Error: fmt.Sprintf("cargo remove: %s", string(out)),
		}}}, nil
	}
	return &codev0.CodeResponse{Result: &codev0.CodeResponse_RemoveDependency{RemoveDependency: &codev0.RemoveDependencyResponse{Success: true}}}, nil
}

// --- Helpers ---

func fixResp(success bool, content, errMsg string, actions []string) *codev0.CodeResponse {
	return &codev0.CodeResponse{Result: &codev0.CodeResponse_Fix{Fix: &codev0.FixResponse{
		Success: success, Content: content, Error: errMsg, Actions: actions,
	}}}
}
