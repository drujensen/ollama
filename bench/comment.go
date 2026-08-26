package main

import (
	"bytes"
	"io"
)

// newCommentStripper allows // line comments in the JSON config file, so the
// config can explain itself the way the old shell script's comments did.
// Comment markers inside strings are left alone.
func newCommentStripper(b []byte) io.Reader {
	var out bytes.Buffer
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out.WriteByte(c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			out.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(b) && b[i+1] == '/' {
			for i < len(b) && b[i] != '\n' {
				i++
			}
			if i < len(b) {
				out.WriteByte('\n')
			}
			continue
		}
		out.WriteByte(c)
	}
	return bytes.NewReader(out.Bytes())
}
