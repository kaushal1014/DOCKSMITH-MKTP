package main
import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type Instruction struct {
	Type string
	Args []string
	Raw  string // full line (important for cache later)
}



func parseDocksmithfile(path string) ([]Instruction, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var instructions []Instruction
	scanner := bufio.NewScanner(file)

	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// skip empty lines
		if line == "" {
			continue
		}

		// skip comments
		if strings.HasPrefix(line, "#") {
			continue
		}

		// Strip inline comments (everything from # onward, but not inside quotes).
		// Simple approach: find first unquoted # and truncate.
		if idx := indexInlineComment(line); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}

		instType := strings.ToUpper(parts[0])

		var args []string

		if instType == "CMD" {
			// preserve full JSON string after CMD
			raw := strings.TrimSpace(line[len(parts[0]):])
			args = []string{raw}
		} else {
			args = parts[1:]
		}

		// validate instruction
		switch instType {
		case "FROM", "COPY", "RUN", "WORKDIR", "ENV", "CMD":
			// valid
		default:
			return nil, fmt.Errorf("unknown instruction at line %d: %s", lineNum, instType)
		}

		instructions = append(instructions, Instruction{
			Type: instType,
			Args: args,
			Raw:  line,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return instructions, nil
}

// indexInlineComment returns the index of the first unquoted '#' in s,
// or -1 if none is found. Handles single-quoted and double-quoted strings.
func indexInlineComment(s string) int {
	inSingle := false
	inDouble := false
	for i, ch := range s {
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return i
			}
		}
	}
	return -1
}