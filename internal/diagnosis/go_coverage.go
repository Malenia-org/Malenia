package diagnosis

import (
	"fmt"
	"strconv"
	"strings"
)

type goCoverageBlock struct {
	file              string
	start, end, count int
}

func parseGoCoverage(data string) ([]goCoverageBlock, error) {
	lines := strings.Split(strings.TrimSpace(data), "\n")
	if len(lines) == 0 || (lines[0] != "mode: set" && lines[0] != "mode: count" && lines[0] != "mode: atomic") {
		return nil, fmt.Errorf("invalid Go coverage mode")
	}
	var blocks []goCoverageBlock
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("invalid Go coverage record")
		}
		at := strings.LastIndexByte(fields[0], ':')
		if at <= 0 {
			return nil, fmt.Errorf("missing Go coverage path")
		}
		var start, startCol, end, endCol int
		span := fields[0][at+1:]
		n, err := fmt.Sscanf(span, "%d.%d,%d.%d", &start, &startCol, &end, &endCol)
		statements, statementErr := strconv.Atoi(fields[1])
		count, countErr := strconv.Atoi(fields[2])
		if n != 4 || err != nil || start < 1 || end < start || startCol < 1 || endCol < 1 ||
			span != fmt.Sprintf("%d.%d,%d.%d", start, startCol, end, endCol) ||
			statementErr != nil || statements < 0 || countErr != nil || count < 0 {
			return nil, fmt.Errorf("invalid Go coverage range or count")
		}
		blocks = append(blocks, goCoverageBlock{fields[0][:at], start, end, count})
	}
	return blocks, nil
}
