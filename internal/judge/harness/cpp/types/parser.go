package types

import (
	"fmt"
	"strings"
	"unicode"
)

const (
	maxTypeLength = 4096
	maxTypeDepth  = 64
)

type typeParser struct {
	input []rune
	pos   int
}

// Parse converts a small, deliberately strict subset of C++ type syntax into
// TypeRef. It supports the type forms accepted by the function harness and is
// designed to grow as runtime types are registered.
func Parse(declaredType string) (TypeRef, error) {
	if len(declaredType) > maxTypeLength {
		return TypeRef{}, fmt.Errorf("type exceeds maximum length of %d bytes", maxTypeLength)
	}
	parser := &typeParser{input: []rune(strings.TrimSpace(declaredType))}
	if len(parser.input) == 0 {
		return TypeRef{}, fmt.Errorf("type is empty")
	}

	ref, err := parser.parseType(0)
	if err != nil {
		return TypeRef{}, err
	}
	parser.skipSpace()
	if parser.pos != len(parser.input) {
		return TypeRef{}, fmt.Errorf("unexpected type suffix %q", string(parser.input[parser.pos:]))
	}
	return ref, nil
}

func (p *typeParser) parseType(depth int) (TypeRef, error) {
	if depth > maxTypeDepth {
		return TypeRef{}, fmt.Errorf("type exceeds maximum template nesting depth of %d", maxTypeDepth)
	}
	p.skipSpace()

	ref := TypeRef{}
	if p.consumeWord("const") {
		ref.Const = true
		p.skipSpace()
	}

	name, err := p.parseName()
	if err != nil {
		return TypeRef{}, err
	}
	if name == "std" {
		p.skipSpace()
		if !p.consumeString("::") {
			return TypeRef{}, fmt.Errorf("expected :: after std")
		}
		name, err = p.parseName()
		if err != nil {
			return TypeRef{}, err
		}
	}
	if name == "long" {
		p.skipSpace()
		if p.consumeWord("long") {
			name = "long long"
		}
	}
	ref.Name = name

	p.skipSpace()
	if p.consumeByte('<') {
		if depth >= maxTypeDepth {
			return TypeRef{}, fmt.Errorf("type exceeds maximum template nesting depth of %d", maxTypeDepth)
		}
		for {
			argument, err := p.parseType(depth + 1)
			if err != nil {
				return TypeRef{}, err
			}
			ref.Arguments = append(ref.Arguments, argument)
			p.skipSpace()
			if p.consumeByte('>') {
				break
			}
			if !p.consumeByte(',') {
				return TypeRef{}, fmt.Errorf("expected , or > in %s", ref.ValueName())
			}
		}
	}

	p.skipSpace()
	if p.consumeWord("const") {
		ref.Const = true
		p.skipSpace()
	}
	if p.consumeString("&&") {
		ref.Reference = RValueReference
	} else if p.consumeByte('&') {
		ref.Reference = LValueReference
	} else if p.consumeByte('*') {
		ref.Pointer = true
	}
	return ref, nil
}

func (p *typeParser) parseName() (string, error) {
	p.skipSpace()
	start := p.pos
	for p.pos < len(p.input) {
		char := p.input[p.pos]
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_' {
			p.pos++
			continue
		}
		break
	}
	if start == p.pos {
		return "", fmt.Errorf("expected type name at %q", string(p.input[p.pos:]))
	}
	return string(p.input[start:p.pos]), nil
}

func (p *typeParser) consumeWord(word string) bool {
	start := p.pos
	if !p.consumeString(word) {
		return false
	}
	if p.pos < len(p.input) {
		next := p.input[p.pos]
		if unicode.IsLetter(next) || unicode.IsDigit(next) || next == '_' {
			p.pos = start
			return false
		}
	}
	return true
}

func (p *typeParser) consumeString(value string) bool {
	pos := p.pos
	for _, expected := range value {
		if pos >= len(p.input) || p.input[pos] != expected {
			return false
		}
		pos++
	}
	p.pos = pos
	return true
}

func (p *typeParser) consumeByte(value rune) bool {
	if p.pos >= len(p.input) || p.input[p.pos] != value {
		return false
	}
	p.pos++
	return true
}

func (p *typeParser) skipSpace() {
	for p.pos < len(p.input) && unicode.IsSpace(p.input[p.pos]) {
		p.pos++
	}
}
