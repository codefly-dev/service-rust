package main

import (
	"reflect"
	"strings"
	"testing"

	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
)

func TestCargoTestArgsHonorsWorkspaceSuiteFilterAndPackage(t *testing.T) {
	request := &runtimev0.TestRequest{
		Target:  "warden-plugins-evidence",
		Suite:   "unit",
		Filters: []string{"route_verifies_the_sdk_carrier_before_appending"},
		Verbose: true,
	}
	got, err := cargoTestArgs(request)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"test",
		"--workspace",
		"--lib",
		"--bins",
		"--verbose",
		"--package",
		"warden-plugins-evidence",
		"route_verifies_the_sdk_carrier_before_appending",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cargo args = %#v, want %#v", got, want)
	}
}

func TestCargoTestArgsMapsDirectoryTargetToManifestPath(t *testing.T) {
	got, err := cargoTestArgs(&runtimev0.TestRequest{
		Target: "./crates/evidence",
		Suite:  "integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"test",
		"--workspace",
		"--tests",
		"--manifest-path",
		"crates/evidence/Cargo.toml",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cargo args = %#v, want %#v", got, want)
	}
}

func TestCargoTestArgsFailsClosedOnUnsupportedSemantics(t *testing.T) {
	cases := map[string]*runtimev0.TestRequest{
		"multiple filters": {
			Filters: []string{"first", "second"},
		},
		"race": {
			Race: true,
		},
		"coverage": {
			Coverage: true,
		},
		"timeout": {
			Timeout: "30s",
		},
		"unknown suite": {
			Suite: "smoke",
		},
		"typed selection": {
			SelectionId: "selection-1",
			Selection: &runtimev0.TestSelection{
				Scope: &runtimev0.TestSelection_Package{
					Package: &runtimev0.TestPackageSelection{Package: "crate-a"},
				},
			},
		},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			args, err := cargoTestArgs(request)
			if err == nil {
				t.Fatalf("cargo args = %#v, want fail-closed error", args)
			}
		})
	}
}

func TestCargoTestArgsNeverTreatsAFlagAsAFilter(t *testing.T) {
	_, err := cargoTestArgs(&runtimev0.TestRequest{Filters: []string{"--ignored"}})
	if err == nil || !strings.Contains(err.Error(), "native substring") {
		t.Fatalf("error = %v, want native-substring rejection", err)
	}
}
