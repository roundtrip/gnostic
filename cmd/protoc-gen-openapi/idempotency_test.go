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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	v3 "github.com/google/gnostic/openapiv3"
)

const idempotencyOption = "idempotency_key_policy_option=tests.idempotency.policy"

func TestOpenAPIIdempotencyPolicy(t *testing.T) {
	pluginPath := filepath.Join(t.TempDir(), "protoc-gen-openapi")
	if output, err := exec.Command("go", "build", "-o", pluginPath, ".").CombinedOutput(); err != nil {
		t.Fatalf("failed to build protoc-gen-openapi: %v\n%s", err, output)
	}

	run := func(t *testing.T, option string, files ...string) (string, string) {
		t.Helper()
		outputDir := t.TempDir()
		args := []string{
			"-I", "../../",
			"-I", "../../third_party",
			"-I", "examples",
			"--plugin=protoc-gen-openapi=" + pluginPath,
		}
		args = append(args, files...)
		args = append(args, "--openapi_out="+option+":"+outputDir)
		output, err := exec.Command("protoc", args...).CombinedOutput()
		if err != nil {
			return "", string(output)
		}
		return outputDir, ""
	}

	readDocument := func(t *testing.T, outputDir, name string) *v3.Document {
		t.Helper()
		contents, err := os.ReadFile(filepath.Join(outputDir, name))
		if err != nil {
			t.Fatalf("read generated OpenAPI document: %v", err)
		}
		doc, err := v3.ParseDocument(contents)
		if err != nil {
			t.Fatalf("parse generated OpenAPI document: %v\n%s", err, contents)
		}
		return doc
	}

	t.Run("optional policy decorates the correct operations", func(t *testing.T) {
		outputDir, stderr := run(t, idempotencyOption, "examples/tests/idempotency/message.proto")
		if stderr != "" {
			t.Fatalf("protoc failed:\n%s", stderr)
		}
		doc := readDocument(t, outputDir, "openapi.yaml")

		for _, operation := range []*v3.Operation{
			findOperation(t, doc, "/v1/messages", "POST"),
			findOperation(t, doc, "/v1/messages/{name}", "PUT"),
			findOperation(t, doc, "/v1/other-messages", "POST"),
		} {
			assertIdempotencyOperation(t, operation)
		}
		for _, operation := range []*v3.Operation{
			findOperation(t, doc, "/v1/messages/{name}", "GET"),
			findOperation(t, doc, "/v1/unsupported", "POST"),
		} {
			if operationHasParameterReference(operation, "#/components/parameters/IdempotencyKey") {
				t.Errorf("operation %q unexpectedly has the idempotency parameter", operation.OperationId)
			}
		}

		assertSingleParameterComponent(t, doc, "IdempotencyKey")
		parameter := findParameterComponent(t, doc, "IdempotencyKey")
		if got := parameter.GetParameter(); got == nil || got.Name != "Idempotency-Key" || got.In != "header" {
			t.Errorf("IdempotencyKey component = %v, want an Idempotency-Key header parameter", parameter)
		}
		assertSingleHeaderComponent(t, doc, "IdempotencyKeyResponse")
		assertSingleHeaderComponent(t, doc, "IdempotencyReplayed")

		created := findResponse(t, findOperation(t, doc, "/v1/messages", "POST"), "200").GetResponse()
		if created == nil || created.Content == nil || findMediaType(created.Content, "application/json") == nil {
			t.Errorf("generated successful response content was not preserved: %v", created)
		}
	})

	t.Run("manual annotations are preserved and component references are resolved", func(t *testing.T) {
		outputDir, stderr := run(t, idempotencyOption, "examples/tests/idempotency/annotated.proto")
		if stderr != "" {
			t.Fatalf("protoc failed:\n%s", stderr)
		}
		doc := readDocument(t, outputDir, "openapi.yaml")
		op := findOperation(t, doc, "/v1/annotated", "POST")

		if got, want := len(op.Parameters), 1; got != want {
			t.Fatalf("annotated operation parameter count = %d, want %d", got, want)
		}
		if ref := op.Parameters[0].GetReference(); ref == nil || ref.XRef != "#/components/parameters/CustomIdempotencyKey" {
			t.Errorf("manual idempotency parameter reference was not preserved: %v", op.Parameters[0])
		}

		created := findResponse(t, op, "201").GetResponse()
		if created == nil {
			t.Fatal("annotated 201 response is not inline")
		}
		manual := findResponseHeader(t, created, "Idempotency-Key")
		if header := manual.GetHeader(); header == nil || header.Description != "A manually documented response header." {
			t.Errorf("manual response header was not preserved: %v", manual)
		}
		assertResponseHeaderReference(t, created, "Idempotency-Replayed", "#/components/headers/IdempotencyReplayed")

		accepted := findResponse(t, op, "202").GetResponse()
		if accepted == nil || accepted.Description != "A reusable accepted response." || accepted.Content == nil {
			t.Errorf("referenced successful response was not cloned with its metadata: %v", accepted)
		}
		assertResponseHeaderReference(t, accepted, "Idempotency-Key", "#/components/headers/IdempotencyKeyResponse")
		assertResponseHeaderReference(t, accepted, "Idempotency-Replayed", "#/components/headers/IdempotencyReplayed")

		component := findResponseComponent(t, doc, "AcceptedMessage").GetResponse()
		if component == nil || component.Headers != nil {
			t.Errorf("shared response component was modified: %v", component)
		}
	})

	t.Run("merged output resolves components from later input files", func(t *testing.T) {
		outputDir, stderr := run(t, idempotencyOption,
			"examples/tests/idempotency/cross_operation.proto",
			"examples/tests/idempotency/cross_components.proto")
		if stderr != "" {
			t.Fatalf("protoc failed:\n%s", stderr)
		}
		doc := readDocument(t, outputDir, "openapi.yaml")
		response := findResponse(t, findOperation(t, doc, "/v1/cross-file", "POST"), "202").GetResponse()
		if response == nil || response.Description != "Declared in a later input file." {
			t.Fatalf("cross-file response was not resolved: %v", response)
		}
		assertResponseHeaderReference(t, response, "Idempotency-Key", "#/components/headers/IdempotencyKeyResponse")
		assertResponseHeaderReference(t, response, "Idempotency-Replayed", "#/components/headers/IdempotencyReplayed")
	})

	t.Run("feature is opt in", func(t *testing.T) {
		outputDir, stderr := run(t, "naming=proto", "examples/tests/idempotency/message.proto")
		if stderr != "" {
			t.Fatalf("protoc failed:\n%s", stderr)
		}
		doc := readDocument(t, outputDir, "openapi.yaml")
		if doc.Components.Parameters != nil || doc.Components.Headers != nil {
			t.Fatalf("output contains idempotency components without configuration: %v", doc.Components)
		}
		if operationHasParameterReference(findOperation(t, doc, "/v1/messages", "POST"), "#/components/parameters/IdempotencyKey") {
			t.Fatal("output contains an idempotency operation parameter without configuration")
		}
	})

	t.Run("source relative output supports multiple files", func(t *testing.T) {
		outputDir, stderr := run(t, "output_mode=source_relative,"+idempotencyOption,
			"examples/tests/idempotency/message.proto",
			"examples/tests/idempotency/second.proto")
		if stderr != "" {
			t.Fatalf("protoc failed:\n%s", stderr)
		}
		messageDoc := readDocument(t, outputDir, "tests/idempotency/message.openapi.yaml")
		assertIdempotencyOperation(t, findOperation(t, messageDoc, "/v1/messages", "POST"))
		secondDoc := readDocument(t, outputDir, "tests/idempotency/second.openapi.yaml")
		assertIdempotencyOperation(t, findOperation(t, secondDoc, "/v1/archive", "POST"))
	})

	for _, test := range []struct {
		name   string
		option string
		want   string
	}{
		{"missing extension", "tests.idempotency.missing", "was not found"},
		{"wrong extendee", "tests.idempotency.wrong_policy_extendee", "must extend google.protobuf.MethodOptions"},
		{"wrong value type", "tests.idempotency.wrong_policy_type", "must be a singular enum extension"},
		{"incompatible enum", "tests.idempotency.incompatible_policy", "enum must define \"POLICY_UNKNOWN\""},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, stderr := run(t, "idempotency_key_policy_option="+test.option, "examples/tests/idempotency/message.proto")
			if !strings.Contains(stderr, test.want) {
				t.Fatalf("protoc error = %q, want it to contain %q", stderr, test.want)
			}
		})
	}

	t.Run("unsupported future enum value", func(t *testing.T) {
		_, stderr := run(t, idempotencyOption, "examples/tests/idempotency/future.proto")
		if !strings.Contains(stderr, "unsupported idempotency policy enum value \"POLICY_FUTURE\"") {
			t.Fatalf("protoc error = %q, want an unsupported enum value error", stderr)
		}
	})
}

func findOperation(t *testing.T, doc *v3.Document, path, method string) *v3.Operation {
	t.Helper()
	for _, named := range doc.Paths.Path {
		if named.Name != path {
			continue
		}
		var operation *v3.Operation
		switch method {
		case "GET":
			operation = named.Value.Get
		case "POST":
			operation = named.Value.Post
		case "PUT":
			operation = named.Value.Put
		case "DELETE":
			operation = named.Value.Delete
		case "PATCH":
			operation = named.Value.Patch
		default:
			t.Fatalf("unsupported test HTTP method %q", method)
		}
		if operation == nil {
			t.Fatalf("operation %s %s was not found", method, path)
		}
		return operation
	}
	t.Fatalf("operation %s %s was not found", method, path)
	return nil
}

func assertIdempotencyOperation(t *testing.T, operation *v3.Operation) {
	t.Helper()
	if !operationHasParameterReference(operation, "#/components/parameters/IdempotencyKey") {
		t.Errorf("operation %q does not reference the idempotency parameter", operation.OperationId)
	}
	response := findResponse(t, operation, "200").GetResponse()
	if response == nil {
		t.Fatalf("operation %q has no inline 200 response", operation.OperationId)
	}
	assertResponseHeaderReference(t, response, "Idempotency-Key", "#/components/headers/IdempotencyKeyResponse")
	assertResponseHeaderReference(t, response, "Idempotency-Replayed", "#/components/headers/IdempotencyReplayed")
}

func operationHasParameterReference(operation *v3.Operation, want string) bool {
	for _, parameter := range operation.Parameters {
		if ref := parameter.GetReference(); ref != nil && ref.XRef == want {
			return true
		}
	}
	return false
}

func findResponse(t *testing.T, operation *v3.Operation, status string) *v3.ResponseOrReference {
	t.Helper()
	for _, response := range operation.Responses.ResponseOrReference {
		if response.Name == status {
			return response.Value
		}
	}
	t.Fatalf("operation %q response %s was not found", operation.OperationId, status)
	return nil
}

func findResponseHeader(t *testing.T, response *v3.Response, name string) *v3.HeaderOrReference {
	t.Helper()
	if response.Headers != nil {
		for _, header := range response.Headers.AdditionalProperties {
			if strings.EqualFold(header.Name, name) {
				return header.Value
			}
		}
	}
	t.Fatalf("response header %q was not found", name)
	return nil
}

func assertResponseHeaderReference(t *testing.T, response *v3.Response, name, want string) {
	t.Helper()
	header := findResponseHeader(t, response, name)
	if ref := header.GetReference(); ref == nil || ref.XRef != want {
		t.Errorf("response header %q = %v, want reference %q", name, header, want)
	}
}

func findParameterComponent(t *testing.T, doc *v3.Document, name string) *v3.ParameterOrReference {
	t.Helper()
	if doc.Components.Parameters != nil {
		for _, parameter := range doc.Components.Parameters.AdditionalProperties {
			if parameter.Name == name {
				return parameter.Value
			}
		}
	}
	t.Fatalf("parameter component %q was not found", name)
	return nil
}

func assertSingleParameterComponent(t *testing.T, doc *v3.Document, name string) {
	t.Helper()
	count := 0
	if doc.Components.Parameters != nil {
		for _, parameter := range doc.Components.Parameters.AdditionalProperties {
			if parameter.Name == name {
				count++
			}
		}
	}
	if count != 1 {
		t.Errorf("parameter component %q count = %d, want 1", name, count)
	}
}

func assertSingleHeaderComponent(t *testing.T, doc *v3.Document, name string) {
	t.Helper()
	count := 0
	if doc.Components.Headers != nil {
		for _, header := range doc.Components.Headers.AdditionalProperties {
			if header.Name == name {
				count++
			}
		}
	}
	if count != 1 {
		t.Errorf("header component %q count = %d, want 1", name, count)
	}
}

func findResponseComponent(t *testing.T, doc *v3.Document, name string) *v3.ResponseOrReference {
	t.Helper()
	if doc.Components.Responses != nil {
		for _, response := range doc.Components.Responses.AdditionalProperties {
			if response.Name == name {
				return response.Value
			}
		}
	}
	t.Fatalf("response component %q was not found", name)
	return nil
}

func findMediaType(mediaTypes *v3.MediaTypes, name string) *v3.MediaType {
	for _, mediaType := range mediaTypes.AdditionalProperties {
		if mediaType.Name == name {
			return mediaType.Value
		}
	}
	return nil
}
