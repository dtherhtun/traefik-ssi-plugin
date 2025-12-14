package ssi

import (
	"bytes"
	"regexp"
)

var (
	includeRegex = regexp.MustCompile(`<!--#\s*include\s+(virtual|file)=["']([^"']+)["']\s*-->`)
	tokenStart   = []byte("<!--#")
	tokenEnd     = []byte("-->")
	maxTagLength = 1024 // Safety limit for a tag size
)

type SegmentType int

const (
	SegmentStatic SegmentType = iota
	SegmentInclude
)

type Segment struct {
	Include *Include // For Include
	Content []byte   // For Static
	Type    SegmentType
}

type Include struct {
	Type     string
	Path     string
	Original string
}

type StreamScanner struct {
	buffer []byte
}

func (s *StreamScanner) Scan(chunk []byte) []Segment {
	// Pre-allocate buffer on first use to reduce allocations
	if s.buffer == nil {
		s.buffer = make([]byte, 0, 4096) // 4KB initial capacity
	}
	s.buffer = append(s.buffer, chunk...)
	segments := make([]Segment, 0, 16) // Pre-allocate for typical page

	for {
		startIdx := bytes.Index(s.buffer, tokenStart)
		if startIdx == -1 {
			// No Start Tag found.
			// We can safely flush everything except the last few bytes
			// that might form the start of a tag.
			safeLen := len(s.buffer) - len(tokenStart) + 1
			if safeLen < 0 {
				safeLen = 0
			}

			if safeLen > 0 {
				segments = append(segments, Segment{
					Type:    SegmentStatic,
					Content: s.buffer[:safeLen],
				})
				s.buffer = s.buffer[safeLen:]
			}
			break
		}

		// We found a start tag at startIdx.
		// If there is content before it, that's a static segment.
		if startIdx > 0 {
			segments = append(segments, Segment{
				Type:    SegmentStatic,
				Content: s.buffer[:startIdx],
			})
			s.buffer = s.buffer[startIdx:]
			// startIdx is now 0 relative to new buffer
			startIdx = 0
		}

		endIdx := bytes.Index(s.buffer, tokenEnd)
		if endIdx == -1 {
			// Tag started but not finished.
			// Check max length to prevent DOS
			if len(s.buffer) > maxTagLength {
				// Too long to be a valid tag, flush it as static text to avoid blocking forever
				// Flush just the first byte to make progress
				segments = append(segments, Segment{
					Type:    SegmentStatic,
					Content: s.buffer[:1],
				})
				s.buffer = s.buffer[1:]
				continue
			}
			break // Need more data
		}

		totalEnd := endIdx + len(tokenEnd)
		tagContent := s.buffer[:totalEnd]

		// Parse the tag
		if m := includeRegex.FindSubmatch(tagContent); m != nil {
			segments = append(segments, Segment{
				Type: SegmentInclude,
				Include: &Include{
					Type:     string(m[1]),
					Path:     string(m[2]),
					Original: string(tagContent),
				},
			})
		} else {
			// Malformed or different tag starting with <!--#include (unlikely but possible)
			segments = append(segments, Segment{
				Type:    SegmentStatic,
				Content: tagContent,
			})
		}

		s.buffer = s.buffer[totalEnd:]
	}
	return segments
}

// Flush returns any remaining bytes in the buffer as a static segment
func (s *StreamScanner) Flush() []Segment {
	if len(s.buffer) == 0 {
		return nil
	}
	seg := Segment{Type: SegmentStatic, Content: s.buffer}
	s.buffer = nil
	return []Segment{seg}
}
