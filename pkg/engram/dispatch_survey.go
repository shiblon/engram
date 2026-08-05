package engram

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Hardcoding each provider's flags means a version bump can only be fixed by a new
// engram release, which is the wrong coupling for a tool whose whole pitch is that
// it works alongside whatever you already use. So an agent reads the provider's
// help and works out how to invoke it headless. Survey is engram's half of that
// division of labor: it gathers the text deterministically and digests it. The
// judgment -- deciding that the model flag is -m -- stays with the agent.

// MaxSurveySubcommands caps how many subcommand help pages a survey walks, so a
// CLI with a large command tree cannot turn one survey into fifty spawns.
const MaxSurveySubcommands = 12

// HelpPage is one captured help output.
type HelpPage struct {
	// Argv is exactly what was run, so the agent can reproduce it.
	Argv []string `json:"argv"`
	// Subcommand is empty for the top-level page.
	Subcommand string `json:"subcommand,omitempty"`
	ExitCode   int    `json:"exit_code"`
	Text       string `json:"text"`
	Error      string `json:"error,omitempty"`
}

// Survey is everything engram could gather about a provider CLI without making
// any judgment about it.
type Survey struct {
	Executable string     `json:"executable"`
	Version    string     `json:"version,omitempty"`
	Pages      []HelpPage `json:"pages"`
	// Digest covers every captured page, so a later survey can tell whether the
	// documented surface moved. Same mechanism the automation catalog uses:
	// persist the judgment with a digest, re-derive on drift.
	Digest string `json:"digest"`
	// Notes records what the walk could not do, never a conclusion about flags.
	Notes []string `json:"notes,omitempty"`
}

// SurveyCLI captures a provider's top-level help plus the help for each
// subcommand, because top-level help misleads about subcommands: codex documents
// -a/--ask-for-approval at the top level and `codex exec` rejects it. Walking
// subcommand help is mandatory for a learner, not optional.
//
// Pass explicit subcommands to walk exactly those; pass none and the candidates
// are read off the top-level help, which is a guess offered to the agent rather
// than a claim about the CLI's structure.
func SurveyCLI(ctx context.Context, executable string, subcommands []string) (Survey, error) {
	survey := Survey{Executable: executable}

	top := captureHelp(ctx, executable, "")
	survey.Pages = append(survey.Pages, top)
	if top.Error != "" && top.Text == "" {
		return survey, fmt.Errorf("could not read help for %q: %s", executable, top.Error)
	}
	if version, err := captureVersion(ctx, executable); err != nil {
		survey.Notes = append(survey.Notes, fmt.Sprintf("could not read --version: %v", err))
	} else {
		survey.Version = version
	}

	candidates := subcommands
	if len(candidates) == 0 {
		candidates = guessSubcommands(top.Text)
		if len(candidates) > 0 {
			survey.Notes = append(survey.Notes, "the subcommands below were guessed from the top-level help layout; "+
				"pass --subcommand to walk a specific one this guess missed")
		}
	}
	if len(candidates) > MaxSurveySubcommands {
		survey.Notes = append(survey.Notes, fmt.Sprintf("%d subcommand candidates were found and %d were walked; "+
			"name the rest explicitly with --subcommand", len(candidates), MaxSurveySubcommands))
		candidates = candidates[:MaxSurveySubcommands]
	}
	for _, subcommand := range candidates {
		survey.Pages = append(survey.Pages, captureHelp(ctx, executable, subcommand))
	}

	var digestSource strings.Builder
	for _, page := range survey.Pages {
		digestSource.WriteString(strings.Join(page.Argv, " "))
		digestSource.WriteString("\n")
		digestSource.WriteString(page.Text)
		digestSource.WriteString("\n")
	}
	survey.Digest = HelpDigest(digestSource.String())
	return survey, nil
}

// captureHelp runs one help invocation. A nonzero exit is recorded rather than
// treated as failure: plenty of CLIs exit nonzero after printing help.
func captureHelp(ctx context.Context, executable, subcommand string) HelpPage {
	argv := []string{executable}
	if subcommand != "" {
		argv = append(argv, subcommand)
	}
	argv = append(argv, "--help")

	page := HelpPage{Argv: argv, Subcommand: subcommand}
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = strings.NewReader("")
	configureProcessGroup(cmd)
	cmd.Cancel = func() error {
		terminateProcessGroup(cmd, time.Second)
		return nil
	}
	if err := cmd.Run(); err != nil {
		page.Error = err.Error()
	}
	if cmd.ProcessState != nil {
		page.ExitCode = cmd.ProcessState.ExitCode()
	}
	// Help goes to stdout on some CLIs and stderr on others; the agent wants
	// whichever one carried it.
	page.Text = strings.TrimSpace(stdout.String())
	if page.Text == "" {
		page.Text = strings.TrimSpace(stderr.String())
	}
	return page
}

func captureVersion(ctx context.Context, executable string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, executable, "--version")
	cmd.Stdin = strings.NewReader("")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return normalizeVersion(string(output)), nil
}

// subcommandLinePattern matches the usual "  name    description" layout of a
// command list. It is deliberately conservative and deliberately not authoritative:
// a miss costs one --subcommand flag, whereas an over-eager match costs a spawn.
var subcommandLinePattern = regexp.MustCompile(`^\s{2,}([a-z][a-z0-9-]{1,20})(\s{2,}\S|$)`)

// commandSectionPattern finds the heading that precedes a command list.
var commandSectionPattern = regexp.MustCompile(`(?i)^\s*(available\s+)?(commands|subcommands)\s*:?\s*$`)

// guessSubcommands reads candidate subcommand names off a help page. It only
// looks inside a commands section, so flag descriptions and examples elsewhere on
// the page do not become spawns.
func guessSubcommands(help string) []string {
	var found []string
	seen := map[string]bool{}
	inSection := false
	for _, line := range strings.Split(help, "\n") {
		if commandSectionPattern.MatchString(line) {
			inSection = true
			continue
		}
		if inSection && strings.TrimSpace(line) == "" {
			continue
		}
		if inSection && !strings.HasPrefix(line, " ") {
			inSection = false
			continue
		}
		if !inSection {
			continue
		}
		match := subcommandLinePattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		name := match[1]
		switch name {
		case "help", "completion", "version":
			continue
		}
		if !seen[name] {
			seen[name] = true
			found = append(found, name)
		}
	}
	sort.Strings(found)
	return found
}
