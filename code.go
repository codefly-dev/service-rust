package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	corecode "github.com/codefly-dev/core/code"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
	runners "github.com/codefly-dev/core/runners/base"
)

var cargoEditionPattern = regexp.MustCompile(`(?m)^\s*edition\s*=\s*["'](2015|2018|2021|2024)["']`)

// Code adds rustfmt to core's secure file/edit/git implementation. rustfmt
// consumes stdin and emits stdout, leaving core as the only workspace writer.
type Code struct {
	*corecode.DefaultCodeServer
	service     *Service
	initialized bool
}

func NewCode(service *Service) *Code {
	return &Code{service: service, DefaultCodeServer: corecode.NewDefaultCodeServer(".")}
}

func (c *Code) Execute(ctx context.Context, request *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	c.ensureInit()
	return c.DefaultCodeServer.Execute(ctx, request)
}

func (c *Code) ensureInit() {
	if c.initialized {
		return
	}
	c.DefaultCodeServer = corecode.NewDefaultCodeServer(c.sourceDir(), corecode.WithSourceFixer(c.fixRust))
	c.initialized = true
}

func (c *Code) sourceDir() string {
	if c.service.sourceLocation != "" {
		return c.service.sourceLocation
	}
	if wd := os.Getenv("CODEFLY_AGENT_WORKDIR"); wd != "" {
		return filepath.Join(wd, c.service.Settings.RustSourceDir())
	}
	return filepath.Join(c.service.Location, c.service.Settings.RustSourceDir())
}

func (c *Code) fixRust(ctx context.Context, input corecode.FixInput) (corecode.FixResult, error) {
	edition := rustEdition(c.sourceDir(), input.Path)
	formatted, diagnostics, err := runners.RunInput(ctx, c.runnerEnvironment(ctx), c.sourceDir(), input.Content,
		"rustfmt", "--emit", "stdout", "--edition", edition)
	if err != nil {
		return corecode.FixResult{}, fmt.Errorf("rustfmt: %w: %s", err, strings.TrimSpace(string(diagnostics)))
	}
	return corecode.FixResult{Content: formatted, Actions: []string{"rustfmt"}, Output: strings.TrimSpace(string(diagnostics))}, nil
}

func (c *Code) runnerEnvironment(ctx context.Context) runners.RunnerEnvironment {
	if c.service.activeEnv != nil {
		return c.service.activeEnv
	}
	var runtimeContext *basev0.RuntimeContext
	if c.service.Base != nil && c.service.Base.Runtime != nil {
		runtimeContext = c.service.Base.Runtime.RuntimeContext
	}
	root := c.service.Location
	if root == "" {
		root = c.sourceDir()
	}
	return runners.ResolveStandaloneEnvironment(ctx, root, runtimeContext)
}

func rustEdition(sourceDir, sourcePath string) string {
	dir := filepath.Dir(filepath.Join(sourceDir, filepath.Clean(sourcePath)))
	for {
		manifest := filepath.Join(dir, "Cargo.toml")
		if data, err := os.ReadFile(manifest); err == nil {
			if match := cargoEditionPattern.FindSubmatch(data); len(match) == 2 {
				return string(match[1])
			}
		}
		if dir == sourceDir || filepath.Dir(dir) == dir {
			break
		}
		dir = filepath.Dir(dir)
	}
	return "2021"
}
