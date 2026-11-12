package diagnosis

import (
	"debug/dwarf"
	"debug/elf"
	"debug/macho"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/causent/malenia/internal/localization"
)

// InstructionResolution is the strongest mapping available from a race stack
// frame and the exact tested ELF. It maps an offset to a machine address and
// DWARF source line; it does not claim an exact memory expression or object.
type InstructionResolution struct {
	Status          string `json:"status"`
	BinaryPath      string `json:"binary_path,omitempty"`
	FunctionSymbol  string `json:"function_symbol,omitempty"`
	InstructionAddr uint64 `json:"instruction_address,omitempty"`
	SourceFile      string `json:"source_file,omitempty"`
	SourceLine      int    `json:"source_line,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

type goBinarySymbol struct {
	name  string
	value uint64
}

const (
	InstructionResolved   = "resolved"
	InstructionUnresolved = "unresolved"
	InstructionAmbiguous  = "ambiguous"
)

// ResolveRaceFrameInstruction resolves the function-relative offset printed
// by the Go race detector against the exact ELF and its DWARF line table.
// Missing/mismatched symbols remain unresolved; source-line proximity is not
// promoted to an exact access.
func ResolveRaceFrameInstruction(binaryPath string, frame localization.Frame) InstructionResolution {
	result := InstructionResolution{Status: InstructionUnresolved, BinaryPath: binaryPath}
	if !frame.PCOffsetPresent {
		result.Reason = "race frame did not preserve a function-relative PC offset"
		return result
	}
	symbols, dwarfData, err := readGoBinaryMetadata(binaryPath)
	if err != nil {
		result.Reason = fmt.Sprintf("read exact binary symbols/DWARF: %v", err)
		return result
	}
	wanted := strings.TrimSuffix(strings.TrimSpace(frame.Function), "()")
	var matches []goBinarySymbol
	for _, symbol := range symbols {
		if symbol.value == 0 {
			continue
		}
		name := strings.TrimPrefix(strings.TrimSuffix(symbol.name, "()"), "_")
		if name == wanted || strings.HasSuffix(name, wanted) {
			matches = append(matches, symbol)
		}
	}
	if len(matches) == 0 {
		result.Reason = "function symbol was not found in the exact binary"
		return result
	}
	if len(matches) > 1 {
		// A suffix match can identify multiple instantiations or wrappers. An
		// exact function name is preferred; otherwise preserve ambiguity.
		exact := matches[:0]
		for _, symbol := range matches {
			if strings.TrimPrefix(strings.TrimSuffix(symbol.name, "()"), "_") == wanted {
				exact = append(exact, symbol)
			}
		}
		if len(exact) == 1 {
			matches = exact
		} else {
			result.Status = InstructionAmbiguous
			result.Reason = "multiple function symbols matched the race frame"
			return result
		}
	}
	pc := matches[0].value + frame.PCOffset
	result.FunctionSymbol = matches[0].name
	result.InstructionAddr = pc

	reader, err := dwarfData.LineReader(nil)
	if err != nil {
		result.Reason = fmt.Sprintf("read DWARF line table: %v", err)
		return result
	}
	var last *dwarf.LineEntry
	for {
		entry := dwarf.LineEntry{}
		err := reader.Next(&entry)
		if err == dwarf.ErrUnknownPC || err != nil {
			break
		}
		if entry.Address > pc {
			break
		}
		if entry.File != nil && entry.Line > 0 {
			copy := entry
			last = &copy
		}
	}
	if last == nil || last.File == nil {
		result.Reason = "instruction address has no DWARF source line"
		return result
	}
	result.Status = InstructionResolved
	result.SourceFile = filepath.ToSlash(last.File.Name)
	result.SourceLine = last.Line
	return result
}

// ResolveRaceReportInstructions joins a parsed race report to a locally
// downloaded binary only when the provider identity agrees. A missing or
// mismatched artifact is an unresolved result, never a source-level guess.
func ResolveRaceReportInstructions(report *RaceReport, evidence []EvidenceItem) {
	if report == nil {
		return
	}
	var binaryPaths []string
	for _, item := range evidence {
		if item.Kind != "race_test_binary" || item.Source == "" {
			continue
		}
		var descriptor ArtifactDescriptor
		if err := json.Unmarshal([]byte(item.Detail), &descriptor); err != nil {
			continue
		}
		if descriptor.SourceRevision == "" || report.SourceRevision == "" || descriptor.SourceRevision != report.SourceRevision {
			continue
		}
		if report.BuildID != "" && descriptor.BuildID != "" && descriptor.BuildID != report.BuildID {
			continue
		}
		binaryPaths = append(binaryPaths, item.Source)
	}
	if len(binaryPaths) != 1 {
		reason := "no uniquely identified race binary matched report revision/build"
		if len(binaryPaths) > 1 {
			reason = "multiple race binaries matched report revision/build"
		}
		report.ReadInstruction = InstructionResolution{Status: InstructionUnresolved, Reason: reason}
		report.WriteInstruction = InstructionResolution{Status: InstructionUnresolved, Reason: reason}
		return
	}
	report.ReadInstruction = ResolveRaceFrameInstruction(binaryPaths[0], firstRaceFrame(report.ReadStack))
	report.WriteInstruction = ResolveRaceFrameInstruction(binaryPaths[0], firstRaceFrame(report.WriteStack))
}

func firstRaceFrame(frames []localization.Frame) localization.Frame {
	if len(frames) == 0 {
		return localization.Frame{}
	}
	return frames[0]
}

func readGoBinaryMetadata(binaryPath string) ([]goBinarySymbol, *dwarf.Data, error) {
	if file, err := elf.Open(binaryPath); err == nil {
		defer file.Close()
		entries, err := file.Symbols()
		if err != nil {
			return nil, nil, fmt.Errorf("read ELF symbols: %w", err)
		}
		symbols := make([]goBinarySymbol, 0, len(entries))
		for _, entry := range entries {
			if elf.ST_TYPE(entry.Info) == elf.STT_FUNC {
				symbols = append(symbols, goBinarySymbol{name: entry.Name, value: entry.Value})
			}
		}
		data, err := file.DWARF()
		return symbols, data, err
	}

	file, err := macho.Open(binaryPath)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	if file.Symtab == nil {
		return nil, nil, fmt.Errorf("Mach-O symbol table is unavailable")
	}
	symbols := make([]goBinarySymbol, 0, len(file.Symtab.Syms))
	for _, entry := range file.Symtab.Syms {
		// Mach-O external/function symbols are sufficient here; DWARF is the
		// authority for the source line after the PC has been recovered.
		if entry.Value != 0 {
			symbols = append(symbols, goBinarySymbol{name: entry.Name, value: entry.Value})
		}
	}
	data, err := file.DWARF()
	return symbols, data, err
}
