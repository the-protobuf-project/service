// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package rustgen

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/the-protobuf-project/service/plugin/generator"
	"google.golang.org/protobuf/compiler/protogen"
)

// Format every file before returning it to protoc. Stdin and explicit settings
// isolate the result from consumer rustfmt.toml files and module discovery.
func formatRust(source string) ([]byte, error) {
	config, err := os.CreateTemp("", "service-rustfmt-*.toml")
	if err != nil {
		return nil, fmt.Errorf("create rustfmt config: %w", err)
	}
	defer func() { _ = os.Remove(config.Name()) }()
	if err := config.Close(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "rustfmt", "--edition", "2024", "--emit", "stdout", "--config-path", config.Name(), "--config", "skip_children=true,style_edition=2024,use_field_init_shorthand=true")
	cmd.Stdin = strings.NewReader(source)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	formatted, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("rustfmt failed (install with rustup component add rustfmt): %w: %s", err, stderr.String())
	}
	return formatted, nil
}

func (t *Target) emit(p *protogen.Plugin, name string, sources []string, note, body string) error {
	source := generator.Banner(p, t.Versions, sources, note) + "\n\n" + body
	formatted, err := formatRust(source)
	if err != nil {
		return fmt.Errorf("service: format %s: %w", name, err)
	}
	_, err = p.NewGeneratedFile(name, "").Write(formatted)
	return err
}
