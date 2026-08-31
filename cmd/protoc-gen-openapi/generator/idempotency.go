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
	"fmt"
	"strings"

	v3 "github.com/google/gnostic/openapiv3"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

const (
	// idempotencyParameterComponent names the request-header parameter component,
	// while idempotencyKeyHeaderComponent names the response-header component that
	// echoes the key back. They live in different reference namespaces but are kept
	// intentionally distinct so neither can accidentally shadow the other.
	idempotencyParameterComponent = "IdempotencyKey"
	idempotencyKeyHeaderComponent = "IdempotencyKeyResponse"
	idempotencyReplayComponent    = "IdempotencyReplayed"

	idempotencyUnknownEnumValue     protoreflect.Name = "POLICY_UNKNOWN"
	idempotencyUnsupportedEnumValue protoreflect.Name = "POLICY_UNSUPPORTED"
	idempotencyOptionalEnumValue    protoreflect.Name = "POLICY_OPTIONAL"
)

const (
	idempotencyPolicyUnknown idempotencyPolicy = iota
	idempotencyPolicyUnsupported
	idempotencyPolicyOptional
)

type idempotencyPolicy uint8

func (g *OpenAPIv3Generator) initializeIdempotencyExtension() error {
	g.idempotencyExtension = nil
	g.idempotencyResolver = nil
	g.idempotencyPolicies = nil

	if g.conf.IdempotencyKeyPolicyOption == nil || *g.conf.IdempotencyKeyPolicyOption == "" {
		return nil
	}

	name := protoreflect.FullName(strings.TrimPrefix(*g.conf.IdempotencyKeyPolicyOption, "."))
	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{File: g.plugin.Request.ProtoFile})
	if err != nil {
		return fmt.Errorf("build descriptor registry for idempotency policy option: %w", err)
	}
	desc, err := files.FindDescriptorByName(name)
	if err != nil {
		return fmt.Errorf("configured idempotency policy option %q was not found in the CodeGeneratorRequest: %w", name, err)
	}
	ext, ok := desc.(protoreflect.ExtensionDescriptor)
	if !ok {
		return fmt.Errorf("configured idempotency policy option %q is not an extension", name)
	}
	methodOptionsName := (&descriptorpb.MethodOptions{}).ProtoReflect().Descriptor().FullName()
	if ext.ContainingMessage() == nil || ext.ContainingMessage().FullName() != methodOptionsName {
		return fmt.Errorf("configured idempotency policy option %q must extend google.protobuf.MethodOptions", name)
	}
	if ext.Kind() != protoreflect.EnumKind || ext.Cardinality() == protoreflect.Repeated {
		return fmt.Errorf("configured idempotency policy option %q must be a singular enum extension", name)
	}

	policies := make(map[protoreflect.EnumNumber]idempotencyPolicy, 3)
	for _, expected := range []struct {
		name   protoreflect.Name
		policy idempotencyPolicy
	}{
		{idempotencyUnknownEnumValue, idempotencyPolicyUnknown},
		{idempotencyUnsupportedEnumValue, idempotencyPolicyUnsupported},
		{idempotencyOptionalEnumValue, idempotencyPolicyOptional},
	} {
		value := ext.Enum().Values().ByName(expected.name)
		if value == nil {
			return fmt.Errorf("configured idempotency policy option %q enum must define %q", name, expected.name)
		}
		if _, exists := policies[value.Number()]; exists {
			return fmt.Errorf("configured idempotency policy option %q must assign distinct numbers to its required enum values", name)
		}
		policies[value.Number()] = expected.policy
	}

	extension := dynamicpb.NewExtensionType(ext)
	resolver := new(protoregistry.Types)
	if err := resolver.RegisterExtension(extension); err != nil {
		return fmt.Errorf("register idempotency policy option %q: %w", name, err)
	}

	g.idempotencyExtension = extension
	g.idempotencyResolver = resolver
	g.idempotencyPolicies = policies
	return nil
}

func (g *OpenAPIv3Generator) idempotencyPolicy(method *protogen.Method) (idempotencyPolicy, error) {
	if g.idempotencyExtension == nil {
		return idempotencyPolicyUnknown, nil
	}

	encoded, err := proto.Marshal(method.Desc.Options())
	if err != nil {
		return idempotencyPolicyUnknown, fmt.Errorf("marshal options for method %s: %w", method.Desc.FullName(), err)
	}
	options := new(descriptorpb.MethodOptions)
	if err := (proto.UnmarshalOptions{Resolver: g.idempotencyResolver}).Unmarshal(encoded, options); err != nil {
		return idempotencyPolicyUnknown, fmt.Errorf("decode idempotency policy for method %s: %w", method.Desc.FullName(), err)
	}
	extDesc := g.idempotencyExtension.TypeDescriptor()
	if !options.ProtoReflect().Has(extDesc) {
		return idempotencyPolicyUnknown, nil
	}

	number := options.ProtoReflect().Get(extDesc).Enum()
	return g.idempotencyPolicyForNumber(method.Desc.FullName(), number)
}

func (g *OpenAPIv3Generator) idempotencyPolicyForNumber(method protoreflect.FullName, number protoreflect.EnumNumber) (idempotencyPolicy, error) {
	if policy, ok := g.idempotencyPolicies[number]; ok {
		return policy, nil
	}
	if g.idempotencyExtension != nil {
		if value := g.idempotencyExtension.TypeDescriptor().Enum().Values().ByNumber(number); value != nil {
			return idempotencyPolicyUnknown, fmt.Errorf("method %s has unsupported idempotency policy enum value %q", method, value.Name())
		}
	}
	return idempotencyPolicyUnknown, fmt.Errorf("method %s has unrecognized idempotency policy value %d", method, number)
}

func (g *OpenAPIv3Generator) applyIdempotencyPolicy(d *v3.Document, op *v3.Operation, policy idempotencyPolicy) error {
	if policy != idempotencyPolicyOptional {
		return nil
	}

	if !hasIdempotencyParameter(d, op) {
		if err := ensureIdempotencyParameterComponent(d); err != nil {
			return fmt.Errorf("prepare idempotency parameter for operation %q: %w", op.OperationId, err)
		}
		ref := "#/components/parameters/" + idempotencyParameterComponent
		if !hasParameterReference(op, ref) {
			op.Parameters = append(op.Parameters, referenceParameter(ref))
		}
	}
	if op.Responses == nil {
		return nil
	}
	for _, named := range op.Responses.ResponseOrReference {
		if named == nil || !isSuccessfulResponse(named.Name) {
			continue
		}
		response, err := idempotencyResponse(d, named)
		if err != nil {
			return fmt.Errorf("decorate successful response %q for operation %q: %w", named.Name, op.OperationId, err)
		}
		if err := ensureIdempotencyHeaderComponents(d); err != nil {
			return fmt.Errorf("prepare idempotency response headers for operation %q: %w", op.OperationId, err)
		}
		if response.Headers == nil {
			response.Headers = &v3.HeadersOrReferences{}
		}
		addResponseHeader(response.Headers, "Idempotency-Key", "#/components/headers/"+idempotencyKeyHeaderComponent)
		addResponseHeader(response.Headers, "Idempotency-Replayed", "#/components/headers/"+idempotencyReplayComponent)
	}
	return nil
}

// idempotencyResponse returns an inline response that can be safely decorated.
// Referenced component responses are cloned so shared components used by
// unsupported operations are not modified.
func idempotencyResponse(d *v3.Document, named *v3.NamedResponseOrReference) (*v3.Response, error) {
	if named.Value == nil {
		return nil, fmt.Errorf("response has no value")
	}
	if response := named.Value.GetResponse(); response != nil {
		return response, nil
	}

	response, err := resolveResponseReference(d, named.Value, make(map[string]bool))
	if err != nil {
		return nil, err
	}
	response = proto.Clone(response).(*v3.Response)
	named.Value = &v3.ResponseOrReference{Oneof: &v3.ResponseOrReference_Response{Response: response}}
	return response, nil
}

func resolveResponseReference(d *v3.Document, value *v3.ResponseOrReference, seen map[string]bool) (*v3.Response, error) {
	if value == nil {
		return nil, fmt.Errorf("response reference has no value")
	}
	if response := value.GetResponse(); response != nil {
		return response, nil
	}
	ref := value.GetReference()
	if ref == nil || ref.XRef == "" {
		return nil, fmt.Errorf("response is neither an inline response nor a reference")
	}

	const prefix = "#/components/responses/"
	if !strings.HasPrefix(ref.XRef, prefix) {
		return nil, fmt.Errorf("cannot decorate non-local response reference %q", ref.XRef)
	}
	name := strings.TrimPrefix(ref.XRef, prefix)
	if name == "" || strings.Contains(name, "/") {
		return nil, fmt.Errorf("invalid local response reference %q", ref.XRef)
	}
	if seen[ref.XRef] {
		return nil, fmt.Errorf("cyclic response reference %q", ref.XRef)
	}
	seen[ref.XRef] = true

	if d.Components == nil || d.Components.Responses == nil {
		return nil, fmt.Errorf("response component %q was not found", name)
	}
	for _, component := range d.Components.Responses.AdditionalProperties {
		if component != nil && component.Name == name {
			return resolveResponseReference(d, component.Value, seen)
		}
	}
	return nil, fmt.Errorf("response component %q was not found", name)
}

// ensureIdempotencyParameterComponent adds the shared request-header parameter
// component. It is only called when an operation actually references the
// component, so operations that carry a manually documented idempotency
// parameter do not cause an unused component to be emitted.
func ensureIdempotencyParameterComponent(d *v3.Document) error {
	if d.Components == nil {
		d.Components = &v3.Components{}
	}
	if d.Components.Parameters == nil {
		d.Components.Parameters = &v3.ParametersOrReferences{}
	}
	for _, component := range d.Components.Parameters.AdditionalProperties {
		if component == nil || component.Name != idempotencyParameterComponent {
			continue
		}
		parameter := resolveParameterReference(d, component.Value, make(map[string]bool))
		if !isIdempotencyParameter(parameter) || parameter.Schema == nil || parameter.Schema.GetSchema() == nil || parameter.Schema.GetSchema().Type != "string" {
			return fmt.Errorf("component parameter %q already exists but is not a string Idempotency-Key header", idempotencyParameterComponent)
		}
		return nil
	}
	d.Components.Parameters.AdditionalProperties = append(d.Components.Parameters.AdditionalProperties, &v3.NamedParameterOrReference{
		Name: idempotencyParameterComponent,
		Value: &v3.ParameterOrReference{Oneof: &v3.ParameterOrReference_Parameter{Parameter: &v3.Parameter{
			Name:        "Idempotency-Key",
			In:          "header",
			Description: "A unique key for safely retrying this operation. Reuse the same key only with the same endpoint and request body.",
			Schema: &v3.SchemaOrReference{Oneof: &v3.SchemaOrReference_Schema{Schema: &v3.Schema{
				Type: "string", MinLength: 1, MaxLength: 255,
			}}},
			Example: &v3.Any{Yaml: "550e8400-e29b-41d4-a716-446655440000"},
		}}},
	})
	return nil
}

// ensureIdempotencyHeaderComponents adds the shared response-header components.
// It is only called when a successful response is actually decorated.
func ensureIdempotencyHeaderComponents(d *v3.Document) error {
	if d.Components == nil {
		d.Components = &v3.Components{}
	}
	if d.Components.Headers == nil {
		d.Components.Headers = &v3.HeadersOrReferences{}
	}
	if err := addComponentHeader(d, idempotencyKeyHeaderComponent, "The idempotency key associated with the response.", "string"); err != nil {
		return err
	}
	return addComponentHeader(d, idempotencyReplayComponent, "Whether the response was replayed from a previous request.", "boolean")
}

func hasIdempotencyParameter(d *v3.Document, op *v3.Operation) bool {
	for _, value := range op.Parameters {
		if parameter := resolveParameterReference(d, value, make(map[string]bool)); isIdempotencyParameter(parameter) {
			return true
		}
	}
	return false
}

func resolveParameterReference(d *v3.Document, value *v3.ParameterOrReference, seen map[string]bool) *v3.Parameter {
	if value == nil {
		return nil
	}
	if parameter := value.GetParameter(); parameter != nil {
		return parameter
	}
	ref := value.GetReference()
	if ref == nil {
		return nil
	}

	const prefix = "#/components/parameters/"
	if !strings.HasPrefix(ref.XRef, prefix) || seen[ref.XRef] {
		return nil
	}
	name := strings.TrimPrefix(ref.XRef, prefix)
	if name == "" || strings.Contains(name, "/") {
		return nil
	}
	seen[ref.XRef] = true

	if d.Components == nil || d.Components.Parameters == nil {
		return nil
	}
	for _, component := range d.Components.Parameters.AdditionalProperties {
		if component != nil && component.Name == name {
			return resolveParameterReference(d, component.Value, seen)
		}
	}
	return nil
}

func isIdempotencyParameter(parameter *v3.Parameter) bool {
	return parameter != nil && strings.EqualFold(parameter.Name, "Idempotency-Key") && strings.EqualFold(parameter.In, "header")
}

func referenceParameter(ref string) *v3.ParameterOrReference {
	return &v3.ParameterOrReference{Oneof: &v3.ParameterOrReference_Reference{Reference: &v3.Reference{XRef: ref}}}
}

func hasParameterReference(op *v3.Operation, want string) bool {
	for _, parameter := range op.Parameters {
		if ref := parameter.GetReference(); ref != nil && ref.XRef == want {
			return true
		}
	}
	return false
}

func addComponentHeader(d *v3.Document, name, description, schemaType string) error {
	for _, component := range d.Components.Headers.AdditionalProperties {
		if component == nil || component.Name != name {
			continue
		}
		header := resolveHeaderReference(d, component.Value, make(map[string]bool))
		if header == nil || header.Schema == nil || header.Schema.GetSchema() == nil || header.Schema.GetSchema().Type != schemaType {
			return fmt.Errorf("component header %q already exists but does not have schema type %q", name, schemaType)
		}
		return nil
	}
	d.Components.Headers.AdditionalProperties = append(d.Components.Headers.AdditionalProperties, &v3.NamedHeaderOrReference{
		Name: name,
		Value: &v3.HeaderOrReference{Oneof: &v3.HeaderOrReference_Header{Header: &v3.Header{
			Description: description,
			Schema:      &v3.SchemaOrReference{Oneof: &v3.SchemaOrReference_Schema{Schema: &v3.Schema{Type: schemaType}}},
		}}},
	})
	return nil
}

func resolveHeaderReference(d *v3.Document, value *v3.HeaderOrReference, seen map[string]bool) *v3.Header {
	if value == nil {
		return nil
	}
	if header := value.GetHeader(); header != nil {
		return header
	}
	ref := value.GetReference()
	if ref == nil {
		return nil
	}

	const prefix = "#/components/headers/"
	if !strings.HasPrefix(ref.XRef, prefix) || seen[ref.XRef] {
		return nil
	}
	name := strings.TrimPrefix(ref.XRef, prefix)
	if name == "" || strings.Contains(name, "/") {
		return nil
	}
	seen[ref.XRef] = true

	if d.Components == nil || d.Components.Headers == nil {
		return nil
	}
	for _, component := range d.Components.Headers.AdditionalProperties {
		if component != nil && component.Name == name {
			return resolveHeaderReference(d, component.Value, seen)
		}
	}
	return nil
}

func addResponseHeader(headers *v3.HeadersOrReferences, name, ref string) {
	for _, header := range headers.AdditionalProperties {
		if header != nil && strings.EqualFold(header.Name, name) {
			return
		}
	}
	headers.AdditionalProperties = append(headers.AdditionalProperties, &v3.NamedHeaderOrReference{
		Name:  name,
		Value: &v3.HeaderOrReference{Oneof: &v3.HeaderOrReference_Reference{Reference: &v3.Reference{XRef: ref}}},
	})
}

func isSuccessfulResponse(status string) bool {
	if len(status) != 3 || status[0] != '2' {
		return false
	}
	if status == "2XX" {
		return true
	}
	return status[1] >= '0' && status[1] <= '9' && status[2] >= '0' && status[2] <= '9'
}
