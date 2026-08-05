package engram

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Results come from the provider's own output channel. Parsing the provider's
// prose on stdout would be unstable across releases and different for every CLI;
// both installed providers instead expose the final message as a documented,
// structured field, which makes reading it one JSON path rather than a parser.

// providerOutput is the raw material a finished child leaves behind.
type providerOutput struct {
	Stdout     []byte
	Stderr     []byte
	OutputFile string
}

// extractedResult is what a spec's result section yields.
type extractedResult struct {
	Result         string
	TerminalReason string
	CostUSD        float64
	ReportedModel  string
	ProviderError  bool
	Tokens         *TokenUsage
	// Notes records reading problems that are not themselves task failures, such
	// as a configured path that was absent from otherwise valid JSON.
	Notes []string
}

// extractResult reads the final answer and reported metadata out of a child's
// output according to the spec.
func extractResult(spec *ProviderSpec, out providerOutput) (extractedResult, error) {
	var extracted extractedResult

	switch spec.Result.Format {
	case ResultFormatText:
		extracted.Result = strings.TrimSpace(string(out.Stdout))

	case ResultFormatLastMessageFile:
		if out.OutputFile == "" {
			return extracted, fmt.Errorf("spec reports its result in a file, but no output file was allocated")
		}
		data, err := os.ReadFile(out.OutputFile)
		if err != nil {
			return extracted, fmt.Errorf("read last-message file: %w", err)
		}
		extracted.Result = strings.TrimSpace(string(data))

	case ResultFormatJSON:
		var document any
		if err := json.Unmarshal(out.Stdout, &document); err != nil {
			return extracted, fmt.Errorf("parse provider JSON output: %w", err)
		}
		extracted.readPaths(spec, document)

	case ResultFormatJSONL:
		// Scan for the last line that actually carries the result path: a JSONL
		// event stream ends with a summary object, but which line that is varies,
		// so pick by content rather than by position.
		var chosen any
		for _, line := range strings.Split(string(out.Stdout), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var document any
			if err := json.Unmarshal([]byte(line), &document); err != nil {
				continue
			}
			if _, ok := resolveJSONPath(document, spec.Result.JSONPath); ok {
				chosen = document
			}
		}
		if chosen == nil {
			return extracted, fmt.Errorf("no JSONL line carried result path %q", spec.Result.JSONPath)
		}
		extracted.readPaths(spec, chosen)

	default:
		return extracted, fmt.Errorf("unknown result format %q", spec.Result.Format)
	}

	// Model identity from a regex applies to formats that report it in a text
	// preamble rather than in a JSON field. This is client-side metadata echoing
	// the resolved configuration, not the model's own claim about itself.
	if extracted.ReportedModel == "" && spec.Result.ModelRegex != "" {
		pattern, err := regexp.Compile(spec.Result.ModelRegex)
		if err != nil {
			return extracted, fmt.Errorf("result.model_regex does not compile: %w", err)
		}
		combined := string(out.Stdout) + "\n" + string(out.Stderr)
		if match := pattern.FindStringSubmatch(combined); len(match) > 1 {
			extracted.ReportedModel = strings.TrimSpace(match[1])
		}
	}
	return extracted, nil
}

// readPaths pulls each configured path out of a parsed JSON document.
func (e *extractedResult) readPaths(spec *ProviderSpec, document any) {
	if spec.Result.JSONPath != "" {
		if value, ok := resolveJSONPath(document, spec.Result.JSONPath); ok {
			e.Result = strings.TrimSpace(jsonScalarString(value))
		} else {
			e.Notes = append(e.Notes, fmt.Sprintf("result path %q absent from provider output", spec.Result.JSONPath))
		}
	}
	if spec.Result.TerminalReasonPath != "" {
		if value, ok := resolveJSONPath(document, spec.Result.TerminalReasonPath); ok {
			e.TerminalReason = jsonScalarString(value)
		}
	}
	if spec.Result.CostUSDPath != "" {
		if value, ok := resolveJSONPath(document, spec.Result.CostUSDPath); ok {
			if cost, err := strconv.ParseFloat(jsonScalarString(value), 64); err == nil {
				e.CostUSD = cost
			}
		}
	}
	if spec.Result.ErrorPath != "" {
		if value, ok := resolveJSONPath(document, spec.Result.ErrorPath); ok {
			e.ProviderError = jsonTruthy(value)
		}
	}
	if spec.Result.ModelPath != "" {
		if value, ok := resolveJSONPath(document, spec.Result.ModelPath); ok {
			e.ReportedModel = reportedModelFrom(value)
		}
	}
	e.Tokens = tokensFrom(document)
}

// reportedModelFrom reads a model identity out of whatever shape the provider
// used. claude keys its usage object by real model id, so the keys are the
// answer there; a plain string is the answer elsewhere.
func reportedModelFrom(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sortStrings(keys)
		return strings.Join(keys, ",")
	default:
		return jsonScalarString(value)
	}
}

// tokensFrom picks up the token counters both installed providers happen to spell
// the same way. Absent counters simply yield nil; token accounting is a bonus on
// top of the result, never a reason to fail a task.
func tokensFrom(document any) *TokenUsage {
	root, ok := document.(map[string]any)
	if !ok {
		return nil
	}
	source := root
	if usage, ok := root["usage"].(map[string]any); ok {
		source = usage
	}
	usage := TokenUsage{
		Input:         jsonInt(source["input_tokens"]),
		Output:        jsonInt(source["output_tokens"]),
		CacheCreation: jsonInt(source["cache_creation_input_tokens"]),
		CacheRead:     jsonInt(source["cache_read_input_tokens"]),
	}
	if usage == (TokenUsage{}) {
		return nil
	}
	return &usage
}

// resolveJSONPath walks a dot-separated path with bracketed indices, such as
// "a.b[0].c". Deliberately not a full JSONPath implementation: a spec should name
// one field, and a query language here would invite the prose-parsing this design
// exists to avoid.
func resolveJSONPath(document any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}
	current := document
	for _, segment := range strings.Split(path, ".") {
		name, indices := splitIndices(segment)
		if name != "" {
			object, ok := current.(map[string]any)
			if !ok {
				return nil, false
			}
			value, ok := object[name]
			if !ok {
				return nil, false
			}
			current = value
		}
		for _, index := range indices {
			array, ok := current.([]any)
			if !ok || index < 0 || index >= len(array) {
				return nil, false
			}
			current = array[index]
		}
	}
	return current, true
}

var indexPattern = regexp.MustCompile(`\[(\d+)\]`)

func splitIndices(segment string) (string, []int) {
	matches := indexPattern.FindAllStringSubmatch(segment, -1)
	name := indexPattern.ReplaceAllString(segment, "")
	indices := make([]int, 0, len(matches))
	for _, match := range matches {
		n, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		indices = append(indices, n)
	}
	return name, indices
}

func jsonScalarString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprintf("%v", typed)
		}
		return string(data)
	}
}

func jsonTruthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != "" && typed != "false" && typed != "0"
	case float64:
		return typed != 0
	default:
		return value != nil
	}
}

func jsonInt(value any) int {
	if number, ok := value.(float64); ok {
		return int(number)
	}
	return 0
}

// modelMatches decides whether the provider's reported model honors what was
// requested. Containment either way, case-insensitively, because a request is
// often an alias ("haiku") of a reported full id
// ("claude-haiku-4-5-20251001") -- and because claude reports a comma-joined
// key set when more than one model was billed.
func modelMatches(requested, reported string) bool {
	if requested == "" || reported == "" {
		return false
	}
	want := strings.ToLower(strings.TrimSpace(requested))
	for _, candidate := range strings.Split(strings.ToLower(reported), ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if strings.Contains(candidate, want) || strings.Contains(want, candidate) {
			return true
		}
	}
	return false
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
