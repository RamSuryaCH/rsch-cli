package infrastructure

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScanFindsInfrastructureFactsWithProvenance(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM golang:1.24\nEXPOSE 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "deployment.yaml"), []byte("apiVersion: v1\nkind: Deployment\nmetadata:\n  name: api\nspec:\n  image: api:latest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Resources) < 3 {
		t.Fatalf("expected infrastructure facts, got %+v", report.Resources)
	}
	for _, item := range report.Resources {
		if item.Path == "" || item.Line <= 0 {
			t.Fatalf("missing provenance: %+v", item)
		}
	}
}

func TestInfrastructureFactsParseImageAndBlockLabels(t *testing.T) {
	for _, tc := range []struct {
		kind, text, want string
	}{
		{"docker", "FROM --platform=linux/amd64 golang:1.25 AS builder", "golang:1.25"},
		{"terraform", `module "network" {`, "network"},
		{"terraform", `provider "aws" {`, "aws"},
		{"terraform", `resource "aws_instance" "api" {`, "api:aws_instance"},
		{"terraform", `data "aws_vpc" "default" {`, "default:aws_vpc"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got, _ := fact(tc.kind, tc.text)
			if got != tc.want {
				t.Fatalf("fact=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestScanIgnoresSymlinkedInfrastructure(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "Dockerfile")
	if err := os.WriteFile(outside, []byte("FROM outside:latest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "Dockerfile")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	report, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Resources) != 0 {
		t.Fatalf("scanned infrastructure outside workspace: %+v", report.Resources)
	}
}
