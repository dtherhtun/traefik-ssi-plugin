package tests

import (
	"testing"

	"github.com/username/traefik-plugin-ssi/ssi"
)

func TestParseIncludes(t *testing.T) {
	content := []byte(`<html><body><!--#include virtual="/header.html" --> Content <!--#include file="/footer.html" --></body></html>`)
	scanner := &ssi.StreamScanner{}
	segments := scanner.Scan(content)
	// We might need flush
	segments = append(segments, scanner.Flush()...)

	// We expect checking the segments for includes
	includeCount := 0
	for _, s := range segments {
		if s.Type == ssi.SegmentInclude {
			includeCount++
			if includeCount == 1 {
				if s.Include.Path != "/header.html" {
					t.Errorf("First path wrong")
				}
			}
			if includeCount == 2 {
				if s.Include.Path != "/footer.html" {
					t.Errorf("Second path wrong")
				}
			}
		}
	}

	if includeCount != 2 {
		t.Errorf("Expected 2 includes, got %d", includeCount)
	}
}

func TestStreamScanner_SplitTags(t *testing.T) {
	scanner := &ssi.StreamScanner{}

	// Chunk 1: "<html><!--#in"
	c1 := []byte("<html><!--#in")
	segs1 := scanner.Scan(c1)

	var fullStatic []byte
	for _, s := range segs1 {
		if s.Type == ssi.SegmentStatic {
			fullStatic = append(fullStatic, s.Content...)
		} else {
			t.Errorf("Unexpected non-static in chunk 1")
		}
	}

	// Chunk 2: "clude virtual='/foo' -->"
	c2 := []byte("clude virtual='/foo' -->")
	segs2 := scanner.Scan(c2)

	foundInclude := false
	for _, s := range segs2 {
		switch s.Type {
		case ssi.SegmentStatic:
			fullStatic = append(fullStatic, s.Content...)
		case ssi.SegmentInclude:
			foundInclude = true
			if s.Include.Path != "/foo" {
				t.Errorf("Path mismatch: %s", s.Include.Path)
			}
		}
	}

	if !foundInclude {
		t.Fatal("Did not find include in chunk 2")
	}

	if string(fullStatic) != "<html>" {
		t.Errorf("Static content mismatch. Got %q, want <html>", string(fullStatic))
	}
}

func TestStreamScanner_Duplication(t *testing.T) {
	scanner := &ssi.StreamScanner{}
	input := []byte(`<html><body>Start<!--#include virtual="/part.html" -->End</body></html>`)
	segments := scanner.Scan(input)
	segments = append(segments, scanner.Flush()...)

	var output []byte
	for _, s := range segments {
		if s.Type == ssi.SegmentStatic {
			output = append(output, s.Content...)
		} else {
			output = append(output, []byte(s.Include.Original)...)
		}
	}

	if string(output) != string(input) {
		t.Errorf("Mismatch!\nInput : %s\nOutput: %s", input, output)
	}
}

func TestStreamScanner_LooseSyntax(t *testing.T) {
	scanner := &ssi.StreamScanner{}
	input := []byte(`<html><!--# include virtual="/space.html" --></html>`)
	segments := scanner.Scan(input)
	segments = append(segments, scanner.Flush()...)

	foundInclude := false
	for _, s := range segments {
		if s.Type == ssi.SegmentInclude {
			foundInclude = true
			if s.Include.Path != "/space.html" {
				t.Errorf("Expected path /space.html, got %s", s.Include.Path)
			}
		}
	}

	if !foundInclude {
		t.Fatal("Did not find include segment")
	}
}

func TestStreamScanner_NoTags(t *testing.T) {
	scanner := &ssi.StreamScanner{}
	c1 := []byte("Hello World")
	segs := scanner.Scan(c1)
	segs = append(segs, scanner.Flush()...)

	if len(segs) == 0 {
		t.Fatal("Expected Content")
	}

	fullContent := ""
	for _, s := range segs {
		fullContent += string(s.Content)
	}

	if fullContent != "Hello World" {
		t.Errorf("Content mismatch. Got %q", fullContent)
	}
}
