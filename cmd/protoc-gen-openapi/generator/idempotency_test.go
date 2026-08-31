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

package generator

import (
	"strings"
	"testing"

	v3 "github.com/google/gnostic/openapiv3"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestIdempotencyMetadataDoesNotDuplicateOrReplaceExistingMetadata(t *testing.T) {
	doc := &v3.Document{Components: &v3.Components{
		Parameters: &v3.ParametersOrReferences{AdditionalProperties: []*v3.NamedParameterOrReference{
			{Name: idempotencyParameterComponent, Value: referenceParameter("#/components/parameters/CustomIdempotencyKey")},
			{Name: "CustomIdempotencyKey", Value: &v3.ParameterOrReference{Oneof: &v3.ParameterOrReference_Parameter{Parameter: &v3.Parameter{
				Name: "Idempotency-Key", In: "header",
				Schema: &v3.SchemaOrReference{Oneof: &v3.SchemaOrReference_Schema{Schema: &v3.Schema{Type: "string"}}},
			}}}},
		}},
		Headers: &v3.HeadersOrReferences{AdditionalProperties: []*v3.NamedHeaderOrReference{
			{Name: idempotencyKeyHeaderComponent, Value: &v3.HeaderOrReference{Oneof: &v3.HeaderOrReference_Reference{
				Reference: &v3.Reference{XRef: "#/components/headers/CustomIdempotencyKey"},
			}}},
			{Name: "CustomIdempotencyKey", Value: &v3.HeaderOrReference{Oneof: &v3.HeaderOrReference_Header{Header: &v3.Header{
				Schema: &v3.SchemaOrReference{Oneof: &v3.SchemaOrReference_Schema{Schema: &v3.Schema{Type: "string"}}},
			}}}},
		}},
	}}

	for i := 0; i < 2; i++ {
		if err := ensureIdempotencyParameterComponent(doc); err != nil {
			t.Fatalf("ensureIdempotencyParameterComponent() error = %v", err)
		}
		if err := ensureIdempotencyHeaderComponents(doc); err != nil {
			t.Fatalf("ensureIdempotencyHeaderComponents() error = %v", err)
		}
	}
	if got, want := len(doc.Components.Parameters.AdditionalProperties), 2; got != want {
		t.Errorf("parameter component count = %d, want %d", got, want)
	}
	if got := doc.Components.Parameters.AdditionalProperties[0].Value.GetReference().XRef; got != "#/components/parameters/CustomIdempotencyKey" {
		t.Errorf("existing parameter component was replaced with %q", got)
	}
	if got, want := len(doc.Components.Headers.AdditionalProperties), 3; got != want {
		t.Errorf("header component count = %d, want %d", got, want)
	}
	if got := doc.Components.Headers.AdditionalProperties[0].Value.GetReference().XRef; got != "#/components/headers/CustomIdempotencyKey" {
		t.Errorf("existing header component was replaced with %q", got)
	}

	headers := &v3.HeadersOrReferences{AdditionalProperties: []*v3.NamedHeaderOrReference{{
		Name: "idempotency-key",
		Value: &v3.HeaderOrReference{Oneof: &v3.HeaderOrReference_Header{Header: &v3.Header{
			Description: "Manually documented header.",
		}}},
	}}}
	addResponseHeader(headers, "Idempotency-Key", "#/components/headers/IdempotencyKey")
	addResponseHeader(headers, "Idempotency-Replayed", "#/components/headers/IdempotencyReplayed")
	addResponseHeader(headers, "Idempotency-Replayed", "#/components/headers/IdempotencyReplayed")
	if got, want := len(headers.AdditionalProperties), 2; got != want {
		t.Errorf("response header count = %d, want %d", got, want)
	}
	if got := headers.AdditionalProperties[0].Value.GetHeader().Description; got != "Manually documented header." {
		t.Errorf("manual response header was replaced with %q", got)
	}
}

func TestIdempotencyComponentsRejectIncompatibleNameCollisions(t *testing.T) {
	doc := &v3.Document{Components: &v3.Components{
		Parameters: &v3.ParametersOrReferences{AdditionalProperties: []*v3.NamedParameterOrReference{{
			Name: idempotencyParameterComponent,
			Value: &v3.ParameterOrReference{Oneof: &v3.ParameterOrReference_Parameter{Parameter: &v3.Parameter{
				Name: "page", In: "query",
				Schema: &v3.SchemaOrReference{Oneof: &v3.SchemaOrReference_Schema{Schema: &v3.Schema{Type: "integer"}}},
			}}},
		}}},
	}}
	if err := ensureIdempotencyParameterComponent(doc); err == nil || !strings.Contains(err.Error(), "is not a string Idempotency-Key header") {
		t.Fatalf("ensureIdempotencyParameterComponent() error = %v, want collision error", err)
	}

	doc.Components.Headers = &v3.HeadersOrReferences{AdditionalProperties: []*v3.NamedHeaderOrReference{{
		Name: idempotencyReplayComponent,
		Value: &v3.HeaderOrReference{Oneof: &v3.HeaderOrReference_Header{Header: &v3.Header{
			Schema: &v3.SchemaOrReference{Oneof: &v3.SchemaOrReference_Schema{Schema: &v3.Schema{Type: "string"}}},
		}}},
	}}}
	if err := ensureIdempotencyHeaderComponents(doc); err == nil || !strings.Contains(err.Error(), "does not have schema type \"boolean\"") {
		t.Fatalf("ensureIdempotencyHeaderComponents() error = %v, want collision error", err)
	}
}

func TestHasIdempotencyParameterRecognizesInlineAndComponentReferences(t *testing.T) {
	doc := &v3.Document{Components: &v3.Components{Parameters: &v3.ParametersOrReferences{
		AdditionalProperties: []*v3.NamedParameterOrReference{{
			Name: "CustomIdempotencyKey",
			Value: &v3.ParameterOrReference{Oneof: &v3.ParameterOrReference_Parameter{Parameter: &v3.Parameter{
				Name: "Idempotency-Key",
				In:   "header",
			}}},
		}},
	}}}
	for _, parameter := range []*v3.ParameterOrReference{
		referenceParameter("#/components/parameters/CustomIdempotencyKey"),
		{Oneof: &v3.ParameterOrReference_Parameter{Parameter: &v3.Parameter{Name: "idempotency-key", In: "HEADER"}}},
	} {
		if !hasIdempotencyParameter(doc, &v3.Operation{Parameters: []*v3.ParameterOrReference{parameter}}) {
			t.Errorf("hasIdempotencyParameter() = false for %v", parameter)
		}
	}
}

func TestApplyIdempotencyPolicyDecoratesSuccessfulInlineAndReferencedResponses(t *testing.T) {
	accepted := &v3.Response{
		Description: "Reusable accepted response.",
		Content: &v3.MediaTypes{AdditionalProperties: []*v3.NamedMediaType{{
			Name:  "application/json",
			Value: &v3.MediaType{Schema: &v3.SchemaOrReference{Oneof: &v3.SchemaOrReference_Schema{Schema: &v3.Schema{Type: "string"}}}},
		}}},
	}
	doc := &v3.Document{Components: &v3.Components{Responses: &v3.ResponsesOrReferences{
		AdditionalProperties: []*v3.NamedResponseOrReference{{
			Name: "Accepted",
			Value: &v3.ResponseOrReference{Oneof: &v3.ResponseOrReference_Response{
				Response: accepted,
			}},
		}},
	}}}
	manualHeader := &v3.Header{Description: "Manually documented response header."}
	op := &v3.Operation{
		OperationId: "Messaging_Annotated",
		Responses: &v3.Responses{ResponseOrReference: []*v3.NamedResponseOrReference{
			inlineResponse("200", &v3.Response{Description: "OK", Content: &v3.MediaTypes{}}),
			inlineResponse("201", &v3.Response{
				Description: "Created",
				Headers: &v3.HeadersOrReferences{AdditionalProperties: []*v3.NamedHeaderOrReference{{
					Name:  "idempotency-key",
					Value: &v3.HeaderOrReference{Oneof: &v3.HeaderOrReference_Header{Header: manualHeader}},
				}}},
			}),
			referencedResponse("202", "#/components/responses/Accepted"),
			inlineResponse("2XX", &v3.Response{Description: "Other success"}),
			inlineResponse("400", &v3.Response{Description: "Bad request"}),
		}},
	}

	if err := (&OpenAPIv3Generator{}).applyIdempotencyPolicy(doc, op, idempotencyPolicyOptional); err != nil {
		t.Fatalf("applyIdempotencyPolicy() error = %v", err)
	}
	if got, want := len(op.Parameters), 1; got != want {
		t.Fatalf("operation parameter count = %d, want %d", got, want)
	}

	for _, status := range []string{"200", "201", "202", "2XX"} {
		response := findTestResponse(t, op, status).GetResponse()
		if response == nil {
			t.Fatalf("response %s remained a reference", status)
		}
		if !hasTestHeader(response.Headers, "Idempotency-Key") || !hasTestHeader(response.Headers, "Idempotency-Replayed") {
			t.Errorf("response %s does not have both idempotency headers", status)
		}
	}
	created := findTestResponse(t, op, "201").GetResponse()
	if got := created.Headers.AdditionalProperties[0].Value.GetHeader().Description; got != manualHeader.Description {
		t.Errorf("manual response header was replaced with %q", got)
	}
	acceptedClone := findTestResponse(t, op, "202").GetResponse()
	if acceptedClone.Description != accepted.Description || acceptedClone.Content == nil {
		t.Errorf("referenced response metadata was not preserved: %v", acceptedClone)
	}
	if accepted.Headers != nil {
		t.Error("shared response component was modified")
	}
	if headers := findTestResponse(t, op, "400").GetResponse().Headers; headers != nil {
		t.Errorf("unsuccessful response was decorated: %v", headers)
	}
}

func TestApplyIdempotencyPolicyRejectsExternalSuccessfulResponseReference(t *testing.T) {
	op := &v3.Operation{
		OperationId: "Messaging_External",
		Responses: &v3.Responses{ResponseOrReference: []*v3.NamedResponseOrReference{
			referencedResponse("200", "https://example.com/openapi.yaml#/components/responses/OK"),
		}},
	}
	err := (&OpenAPIv3Generator{}).applyIdempotencyPolicy(&v3.Document{}, op, idempotencyPolicyOptional)
	if err == nil || !strings.Contains(err.Error(), "cannot decorate non-local response reference") {
		t.Fatalf("applyIdempotencyPolicy() error = %v, want non-local response error", err)
	}
}

func TestIdempotencyPolicyRejectsUnknownEnumNumber(t *testing.T) {
	generator := &OpenAPIv3Generator{idempotencyPolicies: map[protoreflect.EnumNumber]idempotencyPolicy{
		0: idempotencyPolicyUnknown,
		1: idempotencyPolicyUnsupported,
		2: idempotencyPolicyOptional,
	}}
	_, err := generator.idempotencyPolicyForNumber("tests.idempotency.Messaging.Create", 99)
	if err == nil || !strings.Contains(err.Error(), "unrecognized idempotency policy value 99") {
		t.Fatalf("idempotencyPolicyForNumber() error = %v, want unrecognized value error", err)
	}
}

func TestIsSuccessfulResponse(t *testing.T) {
	for _, test := range []struct {
		status string
		want   bool
	}{
		{"200", true},
		{"201", true},
		{"299", true},
		{"2XX", true},
		{"2xx", false},
		{"20A", false},
		{"default", false},
		{"300", false},
	} {
		if got := isSuccessfulResponse(test.status); got != test.want {
			t.Errorf("isSuccessfulResponse(%q) = %t, want %t", test.status, got, test.want)
		}
	}
}

func inlineResponse(status string, response *v3.Response) *v3.NamedResponseOrReference {
	return &v3.NamedResponseOrReference{
		Name: status,
		Value: &v3.ResponseOrReference{Oneof: &v3.ResponseOrReference_Response{
			Response: response,
		}},
	}
}

func referencedResponse(status, ref string) *v3.NamedResponseOrReference {
	return &v3.NamedResponseOrReference{
		Name: status,
		Value: &v3.ResponseOrReference{Oneof: &v3.ResponseOrReference_Reference{
			Reference: &v3.Reference{XRef: ref},
		}},
	}
}

func findTestResponse(t *testing.T, op *v3.Operation, status string) *v3.ResponseOrReference {
	t.Helper()
	for _, response := range op.Responses.ResponseOrReference {
		if response.Name == status {
			return response.Value
		}
	}
	t.Fatalf("response %s was not found", status)
	return nil
}

func hasTestHeader(headers *v3.HeadersOrReferences, name string) bool {
	if headers == nil {
		return false
	}
	for _, header := range headers.AdditionalProperties {
		if strings.EqualFold(header.Name, name) {
			return true
		}
	}
	return false
}
