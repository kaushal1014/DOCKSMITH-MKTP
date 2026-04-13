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

		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}

		instType := strings.ToUpper(parts[0])
		args := parts[1:]

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
