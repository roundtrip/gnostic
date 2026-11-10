// Copyright (c) 2026 Roundtrip Systems Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"

	v3 "github.com/google/gnostic/openapiv3"
)

const mcpOption = "mcp_exclude_option=tests.mcp.exclude_from_mcp"

type testMCPTool struct {
	Name     string `yaml:"name"`
	ReadOnly bool   `yaml:"readOnly"`
}

func TestOpenAPIMCPTools(t *testing.T) {
	pluginPath := filepath.Join(t.TempDir(), "protoc-gen-openapi")
	if output, err := exec.Command("go", "build", "-o", pluginPath, ".").CombinedOutput(); err != nil {
		t.Fatalf("failed to build protoc-gen-openapi: %v\n%s", err, output)
	}

	run := func(t *testing.T, option string) (*v3.Document, string) {
		t.Helper()
		outputDir := t.TempDir()
		output, err := exec.Command("protoc",
			"-I", "../../",
			"-I", "../../third_party",
			"-I", "examples",
			"--plugin=protoc-gen-openapi="+pluginPath,
			"examples/tests/mcp/message.proto",
			"--openapi_out="+option+":"+outputDir,
		).CombinedOutput()
		if err != nil {
			return nil, fmt.Sprintf("%v\n%s", err, output)
		}
		contents, err := os.ReadFile(filepath.Join(outputDir, "openapi.yaml"))
		if err != nil {
			t.Fatalf("read generated OpenAPI document: %v", err)
		}
		doc, err := v3.ParseDocument(contents)
		if err != nil {
			t.Fatalf("parse generated OpenAPI document: %v\n%s", err, contents)
		}
		return doc, ""
	}

	t.Run("operations are marked unless excluded", func(t *testing.T) {
		doc, stderr := run(t, mcpOption)
		if stderr != "" {
			t.Fatalf("protoc failed:\n%s", stderr)
		}

		for _, test := range []struct {
			path, method string
			want         *testMCPTool
		}{
			{"/v1/messages", "GET", &testMCPTool{Name: "list_messages", ReadOnly: true}},
			{"/v1/messages", "POST", &testMCPTool{Name: "create_message"}},
			{"/v1/messages/{name}", "DELETE", &testMCPTool{Name: "delete_message"}},
			{"/v1/other-messages", "POST", nil},
			{"/v1/hidden", "POST", nil},
			{"/v1/annotated", "POST", &testMCPTool{Name: "custom_tool", ReadOnly: true}},
		} {
			op := findOperation(t, doc, test.path, test.method)
			got := mcpToolExtension(t, op)
			switch {
			case test.want == nil && got != nil:
				t.Errorf("%s %s: x-mcp-tool = %+v, want none", test.method, test.path, *got)
			case test.want != nil && got == nil:
				t.Errorf("%s %s: x-mcp-tool missing, want %+v", test.method, test.path, *test.want)
			case test.want != nil && *got != *test.want:
				t.Errorf("%s %s: x-mcp-tool = %+v, want %+v", test.method, test.path, *got, *test.want)
			}
		}
	})

	t.Run("feature is opt in", func(t *testing.T) {
		doc, stderr := run(t, "naming=json")
		if stderr != "" {
			t.Fatalf("protoc failed:\n%s", stderr)
		}
		if got := mcpToolExtension(t, findOperation(t, doc, "/v1/messages", "GET")); got != nil {
			t.Errorf("x-mcp-tool = %+v without configuration, want none", *got)
		}
	})

	for _, test := range []struct {
		name   string
		option string
		want   string
	}{
		{"missing extension", "tests.mcp.missing", "was not found"},
		{"wrong extendee", "tests.mcp.wrong_exclude_extendee", "must extend google.protobuf.MethodOptions"},
		{"wrong value type", "tests.mcp.wrong_exclude_type", "must be a singular bool extension"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, stderr := run(t, "mcp_exclude_option="+test.option)
			if !strings.Contains(stderr, test.want) {
				t.Fatalf("protoc error = %q, want it to contain %q", stderr, test.want)
			}
		})
	}
}

func mcpToolExtension(t *testing.T, op *v3.Operation) *testMCPTool {
	t.Helper()
	var found *testMCPTool
	for _, ext := range op.SpecificationExtension {
		if ext.GetName() != "x-mcp-tool" {
			continue
		}
		if found != nil {
			t.Fatalf("operation %q has more than one x-mcp-tool extension", op.OperationId)
		}
		found = new(testMCPTool)
		if err := yaml.Unmarshal([]byte(ext.GetValue().GetYaml()), found); err != nil {
			t.Fatalf("decode x-mcp-tool for operation %q: %v", op.OperationId, err)
		}
	}
	return found
}
