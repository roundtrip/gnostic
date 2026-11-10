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

	yaml "go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	v3 "github.com/google/gnostic/openapiv3"
)

// mcpToolExtension is the vendor extension added to operations that are
// exposed as MCP tools.
const mcpToolExtension = "x-mcp-tool"

type mcpTool struct {
	Name     string `yaml:"name"`
	ReadOnly bool   `yaml:"readOnly"`
}

func (g *OpenAPIv3Generator) initializeMCPExcludeExtension() error {
	g.mcpExcludeExtension = nil
	g.mcpExcludeResolver = nil

	if g.conf.MCPExcludeOption == nil || *g.conf.MCPExcludeOption == "" {
		return nil
	}

	name := protoreflect.FullName(strings.TrimPrefix(*g.conf.MCPExcludeOption, "."))
	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{File: g.plugin.Request.ProtoFile})
	if err != nil {
		return fmt.Errorf("build descriptor registry for MCP exclude option: %w", err)
	}
	desc, err := files.FindDescriptorByName(name)
	if err != nil {
		return fmt.Errorf("configured MCP exclude option %q was not found in the CodeGeneratorRequest: %w", name, err)
	}
	ext, ok := desc.(protoreflect.ExtensionDescriptor)
	if !ok {
		return fmt.Errorf("configured MCP exclude option %q is not an extension", name)
	}
	methodOptionsName := (&descriptorpb.MethodOptions{}).ProtoReflect().Descriptor().FullName()
	if ext.ContainingMessage() == nil || ext.ContainingMessage().FullName() != methodOptionsName {
		return fmt.Errorf("configured MCP exclude option %q must extend google.protobuf.MethodOptions", name)
	}
	if ext.Kind() != protoreflect.BoolKind || ext.Cardinality() == protoreflect.Repeated {
		return fmt.Errorf("configured MCP exclude option %q must be a singular bool extension", name)
	}

	extension := dynamicpb.NewExtensionType(ext)
	resolver := new(protoregistry.Types)
	if err := resolver.RegisterExtension(extension); err != nil {
		return fmt.Errorf("register MCP exclude option %q: %w", name, err)
	}

	g.mcpExcludeExtension = extension
	g.mcpExcludeResolver = resolver
	return nil
}

// excludedFromMCP reports whether the method is marked with the configured
// MCP exclude option. The plugin doesn't link the option's Go type, so the
// method options are re-decoded with a resolver that knows the extension.
func (g *OpenAPIv3Generator) excludedFromMCP(method *protogen.Method) (bool, error) {
	encoded, err := proto.Marshal(method.Desc.Options())
	if err != nil {
		return false, fmt.Errorf("marshal options for method %s: %w", method.Desc.FullName(), err)
	}
	options := new(descriptorpb.MethodOptions)
	if err := (proto.UnmarshalOptions{Resolver: g.mcpExcludeResolver}).Unmarshal(encoded, options); err != nil {
		return false, fmt.Errorf("decode MCP exclude option for method %s: %w", method.Desc.FullName(), err)
	}
	extDesc := g.mcpExcludeExtension.TypeDescriptor()
	if !options.ProtoReflect().Has(extDesc) {
		return false, nil
	}
	return options.ProtoReflect().Get(extDesc).Bool(), nil
}

// addMCPTool marks op as an MCP tool named by its operation ID. Tools for GET
// operations are read-only. An x-mcp-tool extension already set by an
// operation annotation is left unchanged.
func addMCPTool(op *v3.Operation, httpMethod string) error {
	for _, ext := range op.SpecificationExtension {
		if ext.GetName() == mcpToolExtension {
			return nil
		}
	}
	b, err := yaml.Marshal(mcpTool{Name: op.OperationId, ReadOnly: httpMethod == "GET"})
	if err != nil {
		return fmt.Errorf("encode %s for operation %q: %w", mcpToolExtension, op.OperationId, err)
	}
	op.SpecificationExtension = append(op.SpecificationExtension, &v3.NamedAny{
		Name:  mcpToolExtension,
		Value: &v3.Any{Yaml: string(b)},
	})
	return nil
}
