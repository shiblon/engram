package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var (
	dispatchProject       bool
	dispatchConfigPath    string
	dispatchDryRun        bool
	dispatchSkipVersion   bool
	dispatchConcurrency   int
	dispatchSpecFrom      string
	dispatchSpecProvider  string
	dispatchProbeModel    string
	dispatchProbeTimeout  int
	dispatchProbeNoSave   bool
	dispatchProbeNoLive   bool
	dispatchProbeKeepCtx  bool
	dispatchSurveySubs    []string
	dispatchSurveyJSON    bool
	dispatchSeedOverwrite bool
)

var dispatchCmd = &cobra.Command{
	Use:   "dispatch",
	Short: "Hand decomposed work to provider CLIs as child processes and collect the results",
	Long: `Run one or more provider CLIs as child processes and join their results.

A dispatch is a batch, not a daemon: one invocation, N children, exit when the work
is done. It adds no database schema, because the supervisor outlives every child and
nothing survives the batch. A run dies with its session, which is deliberate --
detachment is already solved one layer down, so put a long batch in a tmux pane
rather than expecting engram to own daemon code.

Provider invocation is learned, not compiled in. Each provider has an invocation
spec: an argv template with placeholders, stored as an ordinary long-term memory so
you can repair a moved flag by editing JSON instead of waiting for a release. Seeds
ship with engram so a fresh install works, but a seed is a guess until you probe it.

The usual sequence for a new provider:

  engram dispatch survey <exe>              read its help, including subcommands
  engram dispatch spec put <provider>       write the spec you worked out
  engram dispatch probe <provider> --model M smoke it and verify the model flag
  engram dispatch run --config batch.json   fan out

Specs live in the global (~/.engram) database by default, because they describe the
CLIs installed on this machine rather than anything about one repo. Use --project to
read and write a repo-local spec instead.

Status goes to stdout as JSON Lines, one object per line, flushed per line; stderr
carries human output only, and the two are never interleaved.`,
}

var dispatchRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a batch of provider children from a config document and stream status",
	Long: `Run a fan-out described by a JSON config document.

N tasks with per-task prompt, provider, model, and deadline is structured data past
the point where flags should carry it, so the batch arrives as a file: re-runnable,
diffable, and reviewable before it spends tokens. Use --config - to read stdin.

Config shape:

  {
    "v": 1,
    "max_concurrent": 4,
    "deadline_seconds": 900,
    "heartbeat_seconds": 15,
    "defaults": {"provider": "claude", "authority": "read-only"},
    "tasks": [
      {"id": "slice-1", "prompt": "...", "model": "haiku"},
      {"id": "whole",   "prompt_file": "prompts/altitude.txt", "model": "opus"}
    ]
  }

Defaults fill any field a task leaves unset, except the id and prompt: a shared
prompt across every slice would be a decomposition that did not decompose.
Authority is named by role (read-only, edit, default) and the spec maps it to
whatever this provider spells it, so a config stays portable.

Every line of the status stream carries "v" and "type" so a parser fails loudly on
schema drift. The types are batch_start, task_start, status, task_done, batch_done.
Status is emitted on state change and on a heartbeat, because change-only would make
a slow task indistinguishable from a hang. batch_done is authoritative and
self-contained, so a caller that read nothing until exit still gets the whole answer.

--dry-run resolves every invocation and prints the argv it would run without
spawning anything, which validates a whole batch for zero tokens. Do that first.`,
	Args: cobra.NoArgs,
	RunE: runDispatchRun,
}

func runDispatchRun(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	if dispatchConfigPath == "" {
		return fmt.Errorf("--config is required (use - for stdin)")
	}
	data, err := readDispatchInput(cmd, dispatchConfigPath)
	if err != nil {
		return err
	}
	config, err := engram.ParseBatchConfig(data)
	if err != nil {
		return err
	}
	if dispatchConcurrency > 0 {
		config.MaxConcurrent = dispatchConcurrency
	}

	specs, err := resolveBatchSpecs(ctx, *config)
	if err != nil {
		return err
	}

	// Status is a stream on stdout; human notes go to stderr, never interleaved.
	outcome, err := engram.RunBatch(ctx, *config, engram.DispatchOptions{
		Specs:            specs,
		Emitter:          engram.NewEventEmitter(cmd.OutOrStdout(), nil),
		SkipVersionCheck: dispatchSkipVersion,
		DryRun:           dispatchDryRun,
	})
	if err != nil {
		return err
	}
	for _, warning := range outcome.Warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "engram dispatch: %s\n", warning)
	}
	if outcome.State == engram.BatchStateFailed {
		return fmt.Errorf("every task failed; see the task_done lines for why")
	}
	return nil
}

// resolveBatchSpecs looks up one spec per provider the batch names, preferring a
// learned spec memory over the shipped seed.
func resolveBatchSpecs(ctx context.Context, config engram.BatchConfig) (map[string]engram.ResolvedSpec, error) {
	handle, dbErr := openScopeDBReadOnly(ctx, !dispatchProject)
	var db = interfaceDB(handle)
	if handle != nil {
		defer handle.DB.Close()
	}

	specs := map[string]engram.ResolvedSpec{}
	for _, task := range config.Tasks {
		if _, done := specs[task.Provider]; done {
			continue
		}
		resolved, err := engram.ResolveProviderSpec(ctx, db, task.Provider)
		if err != nil {
			if dbErr != nil {
				return nil, fmt.Errorf("%w (no %s memory database was readable: %v)", err, scopeName(!dispatchProject), dbErr)
			}
			return nil, err
		}
		specs[task.Provider] = resolved
	}
	return specs, nil
}

// dispatch spec

var dispatchSpecCmd = &cobra.Command{
	Use:   "spec",
	Short: "Inspect, validate, and store the learned invocation spec for a provider",
	Long: `Manage provider invocation specs.

A spec is an argv template with placeholders plus the handful of fields dispatch
needs: prompt transport, model flag, where the result appears, graded-authority
flags, budget flag, context suppression, working-directory semantics, environment,
and exit-code semantics. It carries in-band provenance recording the version and
help digest it was learned against and which fields were verified rather than
inferred.

It is stored as a long-term memory holding one fenced JSON block. That is the
cheapest possible home -- no new table, no new file location, no new verb -- and it
means repair uses the tool you already reach for: read the memory, edit the JSON,
write it back. Validation happens when dispatch parses the block rather than when
the memory is written, which is the right trade for something meant to be
hand-edited at 11pm.

Substitution goes into already-split argv elements and never into a string that is
split afterward. There is no shell, so a prompt containing semicolons, backticks,
or newlines is simply bytes inside one argument.`,
}

var dispatchSpecListCmd = &cobra.Command{
	Use:   "list",
	Short: "List learned provider specs and the shipped seeds",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := context.Background()
		out := cmd.OutOrStdout()

		learned := map[string]bool{}
		if handle, err := openScopeDBReadOnly(ctx, !dispatchProject); err == nil {
			defer handle.DB.Close()
			specs, problems, err := engram.ListProviderSpecs(ctx, handle.DB)
			if err != nil {
				return err
			}
			for _, problem := range problems {
				fmt.Fprintf(cmd.ErrOrStderr(), "engram dispatch spec: %v\n", problem)
			}
			for _, spec := range specs {
				learned[spec.Provider] = true
				fmt.Fprintf(out, "%s\tlearned\t%s\t%s\n", spec.Provider,
					versionOrUnknown(spec.Provenance.LearnedVersion), probeSummary(spec))
			}
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "engram dispatch spec: no %s memory read: %v\n", scopeName(!dispatchProject), err)
		}

		seeds, err := engram.SeedProviderSpecs()
		if err != nil {
			return err
		}
		for _, provider := range sortedProviders(seeds) {
			if learned[provider] {
				continue
			}
			fmt.Fprintf(out, "%s\tseed\t%s\tunprobed on this machine\n", provider,
				versionOrUnknown(seeds[provider].Provenance.LearnedVersion))
		}
		return nil
	},
}

var dispatchSpecShowCmd = &cobra.Command{
	Use:   "show <provider>",
	Short: "Print the effective invocation spec for a provider as JSON",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		handle, err := openScopeDBReadOnly(ctx, !dispatchProject)
		var db = interfaceDB(handle)
		if handle != nil {
			defer handle.DB.Close()
		}
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "engram dispatch spec: no %s memory read: %v\n", scopeName(!dispatchProject), err)
		}
		resolved, err := engram.ResolveProviderSpec(ctx, db, args[0])
		if err != nil {
			return err
		}
		body, err := resolved.Spec.Marshal()
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "engram dispatch spec: source is the %s\n", resolved.Origin)
		fmt.Fprintln(cmd.OutOrStdout(), string(body))
		return nil
	},
}

var dispatchSpecValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Check a spec without spending tokens",
	Long: `Validate a spec document, either a stored one (--provider) or a candidate
(--from <file|->).

This is the payoff for a structured artifact over a wrapper script: a script can
only be run and observed, whereas a spec can be checked before it spawns anything.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := context.Background()
		switch {
		case dispatchSpecFrom != "" && dispatchSpecProvider != "":
			return fmt.Errorf("pass either --from or --provider, not both")
		case dispatchSpecFrom != "":
			data, err := readDispatchInput(cmd, dispatchSpecFrom)
			if err != nil {
				return err
			}
			body, err := engram.ExtractSpecJSON(string(data))
			if err != nil {
				return err
			}
			spec, err := engram.ParseProviderSpec(body)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "spec for %s is valid\n", spec.Provider)
			return nil
		case dispatchSpecProvider != "":
			handle, err := openScopeDBReadOnly(ctx, !dispatchProject)
			if err != nil {
				return err
			}
			defer handle.DB.Close()
			spec, err := engram.ReadProviderSpec(ctx, handle.DB, dispatchSpecProvider)
			if err != nil {
				return err
			}
			if spec == nil {
				return fmt.Errorf("no %s spec memory for provider %q", scopeName(!dispatchProject), dispatchSpecProvider)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "spec for %s is valid\n", spec.Provider)
			return nil
		default:
			return fmt.Errorf("pass --from <file|-> or --provider <name>")
		}
	},
}

var dispatchSpecPutCmd = &cobra.Command{
	Use:   "put <provider>",
	Short: "Store a validated invocation spec as a long-term memory",
	Long: `Write a spec, read from --from <file|-> (default stdin).

The input may be a bare JSON document or a memory body containing a fenced json
block, so the output of 'spec show' round-trips.

The first learn for a provider is worth a human look, because engram is about to
construct an invocation from text a binary printed. The risk is low -- you installed
the binary -- but reviewed-once is the right amount of friction for something that
will then run unattended.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		source := dispatchSpecFrom
		if source == "" {
			source = "-"
		}
		data, err := readDispatchInput(cmd, source)
		if err != nil {
			return err
		}
		body, err := engram.ExtractSpecJSON(string(data))
		if err != nil {
			return err
		}
		spec, err := engram.ParseProviderSpec(body)
		if err != nil {
			return err
		}
		if spec.Provider != args[0] {
			return fmt.Errorf("spec names provider %q but the command names %q", spec.Provider, args[0])
		}
		handle, err := openScopeDB(ctx, !dispatchProject)
		if err != nil {
			return err
		}
		defer handle.DB.Close()
		if err := engram.WriteProviderSpec(ctx, handle.DB, spec,
			engram.WithCurationScope(scopeName(!dispatchProject))); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "wrote %s spec to %s memory %s\n",
			spec.Provider, scopeName(!dispatchProject), engram.DispatchSpecKey(spec.Provider))
		return nil
	},
}

var dispatchSpecSeedCmd = &cobra.Command{
	Use:   "seed [provider...]",
	Short: "Copy the shipped seed specs into memory so they can be edited",
	Long: `Write engram's shipped seed specs into memory.

Seeds already work without this: dispatch falls back to them when no spec memory
exists. Materializing one is for editing it -- a seed in memory is a starting point
you own, and probing it replaces its provenance with what is true here.

An existing spec memory is left alone unless --overwrite is given, because a learned
and probed spec outranks a shipped guess.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		seeds, err := engram.SeedProviderSpecs()
		if err != nil {
			return err
		}
		wanted := args
		if len(wanted) == 0 {
			wanted = sortedProviders(seeds)
		}
		handle, err := openScopeDB(ctx, !dispatchProject)
		if err != nil {
			return err
		}
		defer handle.DB.Close()

		for _, provider := range wanted {
			seed, ok := seeds[provider]
			if !ok {
				return fmt.Errorf("no seed spec ships for provider %q", provider)
			}
			if !dispatchSeedOverwrite {
				existing, err := engram.ReadProviderSpec(ctx, handle.DB, provider)
				if err == nil && existing != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "%s already has a spec memory; left alone (use --overwrite to replace)\n", provider)
					continue
				}
			}
			if err := engram.WriteProviderSpec(ctx, handle.DB, seed,
				engram.WithCurationScope(scopeName(!dispatchProject))); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s seed to %s memory %s\n",
				provider, scopeName(!dispatchProject), engram.DispatchSpecKey(provider))
		}
		return nil
	},
}

// dispatch probe

var dispatchProbeCmd = &cobra.Command{
	Use:   "probe <provider>",
	Short: "Smoke a spec and positively verify the model flag, then record what held",
	Long: `Probe a provider spec against the CLI installed here.

Learning must probe, not believe. Reading help and trusting the inference has an
expensive silent failure: a misread model flag means every child in a fan-out
quietly runs the default model, and the output looks entirely plausible.

Two phases, and the second is not optional:

  Smoke probe        a trivial prompt, headless, short timeout, context discovery
                     suppressed. The suppression matters: without it the probe costs
                     a quarter in context loading rather than a fraction of a cent.
  Model verification ask for a specific model with --model and confirm the provider's
                     own output reports it. When a provider reports nothing about its
                     effective configuration, an invalid model name is passed instead
                     and expected to error; a provider that silently falls back is
                     itself the finding, recorded as inferred and untrusted.

The model is never verified by asking the child what model it is. A model reporting
its own identity is either reading something its harness injected or guessing, and
the two are indistinguishable from outside -- worse, a harness that injects a static
identity string answers the same whether the flag was honored, ignored, or silently
downgraded. Trustworthy model identity comes from the CLI's own output metadata.

Findings are written back into the spec's provenance unless --no-save is given.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		provider := args[0]

		handle, dbErr := openScopeDB(ctx, !dispatchProject)
		var db = interfaceDB(handle)
		if handle != nil {
			defer handle.DB.Close()
		}
		resolved, err := engram.ResolveProviderSpec(ctx, db, provider)
		if err != nil {
			return err
		}

		keepContext := dispatchProbeKeepCtx
		suppress := !keepContext
		probe, err := engram.ProbeSpec(ctx, resolved.Spec, engram.ProbeOptions{
			Model:            dispatchProbeModel,
			TimeoutSeconds:   dispatchProbeTimeout,
			SuppressContext:  &suppress,
			SkipFlagLiveness: dispatchProbeNoLive,
		})
		if err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "provider:        %s (spec source: %s)\n", probe.Provider, resolved.Origin)
		fmt.Fprintf(out, "installed:       %s\n", versionOrUnknown(probe.Version))
		fmt.Fprintf(out, "smoke:           %s (exit %d)\n", okWord(probe.SmokeOK), probe.ExitCode)
		if probe.RequestedModel != "" {
			fmt.Fprintf(out, "model requested: %s\n", probe.RequestedModel)
			fmt.Fprintf(out, "model reported:  %s\n", versionOrUnknown(probe.ReportedModel))
			fmt.Fprintf(out, "model verified:  %s\n", okWord(probe.ModelVerified))
		}
		if probe.FlagLiveness != "" {
			fmt.Fprintf(out, "flag liveness:   %s\n", probe.FlagLiveness)
		}
		if probe.CostUSD > 0 {
			fmt.Fprintf(out, "cost:            $%.6f\n", probe.CostUSD)
		}
		if probe.Result != "" {
			fmt.Fprintf(out, "result:          %s\n", firstLineOf(probe.Result))
		}
		for _, note := range probe.Notes {
			fmt.Fprintf(cmd.ErrOrStderr(), "engram dispatch probe: %s\n", note)
		}
		if probe.Stderr != "" && !probe.SmokeOK {
			fmt.Fprintf(cmd.ErrOrStderr(), "engram dispatch probe: child stderr:\n%s\n", probe.Stderr)
		}

		if dispatchProbeNoSave {
			return nil
		}
		if dbErr != nil {
			return fmt.Errorf("probe ran but findings could not be saved: no %s memory database: %w",
				scopeName(!dispatchProject), dbErr)
		}
		updated := engram.ApplyProbe(resolved.Spec, probe, time.Now())
		if err := engram.WriteProviderSpec(ctx, handle.DB, updated,
			engram.WithCurationScope(scopeName(!dispatchProject))); err != nil {
			return err
		}
		fmt.Fprintf(out, "saved:           provenance updated in %s memory %s\n",
			scopeName(!dispatchProject), engram.DispatchSpecKey(provider))
		return nil
	},
}

// dispatch survey

var dispatchSurveyCmd = &cobra.Command{
	Use:   "survey <executable>",
	Short: "Capture a provider CLI's help, including subcommands, for a learner to read",
	Long: `Capture help text from a provider CLI so an agent can work out how to invoke
it headless.

This is engram's half of the division of labor: it gathers text deterministically
and digests it, while the judgment -- deciding that the model flag is -m -- stays
with the agent. No conclusion about any flag is drawn here.

Subcommand help is walked, not just the top level, because top-level help misleads:
codex documents -a/--ask-for-approval at the top level and 'codex exec' rejects it.
Candidates are guessed from the help layout; name one explicitly with --subcommand
when the guess misses it.

The digest covers every captured page, so a later survey shows whether the
documented surface moved. Record it in the spec's provenance and drift becomes
detectable rather than assumed absent.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		survey, err := engram.SurveyCLI(ctx, args[0], dispatchSurveySubs)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if dispatchSurveyJSON {
			body, err := json.MarshalIndent(survey, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(out, string(body))
			return nil
		}
		fmt.Fprintf(out, "executable: %s\n", survey.Executable)
		fmt.Fprintf(out, "version:    %s\n", versionOrUnknown(survey.Version))
		fmt.Fprintf(out, "digest:     %s\n\n", survey.Digest)
		for _, page := range survey.Pages {
			heading := "top level"
			if page.Subcommand != "" {
				heading = page.Subcommand
			}
			fmt.Fprintf(out, "=== %s ($ %s) exit %d\n", heading, strings.Join(page.Argv, " "), page.ExitCode)
			if page.Error != "" {
				fmt.Fprintf(out, "(error: %s)\n", page.Error)
			}
			fmt.Fprintf(out, "%s\n\n", page.Text)
		}
		for _, note := range survey.Notes {
			fmt.Fprintf(cmd.ErrOrStderr(), "engram dispatch survey: %s\n", note)
		}
		return nil
	},
}

// helpers

// readDispatchInput reads a file, or stdin when path is "-".
func readDispatchInput(cmd *cobra.Command, path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(cmd.InOrStdin())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// interfaceDB yields the handle's database, or nil when the handle is absent, so a
// missing memory database degrades to seed-only resolution instead of failing.
func interfaceDB(handle *engram.DBHandle) *sql.DB {
	if handle == nil {
		return nil
	}
	return handle.DB
}

func versionOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func okWord(ok bool) string {
	if ok {
		return "yes"
	}
	return "no"
}

func firstLineOf(s string) string {
	if index := strings.IndexByte(s, '\n'); index >= 0 {
		return s[:index] + " ..."
	}
	return s
}

func probeSummary(spec *engram.ProviderSpec) string {
	if spec.Provenance.Probe == nil {
		return "never probed"
	}
	if spec.Provenance.Probe.ModelVerified {
		return "probed, model verified " + spec.Provenance.Probe.At
	}
	return "probed, model unverified " + spec.Provenance.Probe.At
}

func sortedProviders(specs map[string]*engram.ProviderSpec) []string {
	providers := make([]string, 0, len(specs))
	for provider := range specs {
		providers = append(providers, provider)
	}
	for i := 1; i < len(providers); i++ {
		for j := i; j > 0 && providers[j] < providers[j-1]; j-- {
			providers[j], providers[j-1] = providers[j-1], providers[j]
		}
	}
	return providers
}

func init() {
	dispatchCmd.PersistentFlags().BoolVar(&dispatchProject, "project", false,
		"read and write specs in the project (.engram) database instead of the global one")

	dispatchRunCmd.Flags().StringVar(&dispatchConfigPath, "config", "", "batch config document (- for stdin)")
	dispatchRunCmd.Flags().BoolVar(&dispatchDryRun, "dry-run", false,
		"resolve and print every invocation without spawning anything")
	dispatchRunCmd.Flags().BoolVar(&dispatchSkipVersion, "skip-version-check", false,
		"skip the one --version spawn per provider that detects spec drift")
	dispatchRunCmd.Flags().IntVar(&dispatchConcurrency, "max-concurrent", 0,
		"override the config's global concurrency cap")

	dispatchSpecValidateCmd.Flags().StringVar(&dispatchSpecFrom, "from", "", "spec document to validate (- for stdin)")
	dispatchSpecValidateCmd.Flags().StringVar(&dispatchSpecProvider, "provider", "", "validate the stored spec for this provider")
	dispatchSpecPutCmd.Flags().StringVar(&dispatchSpecFrom, "from", "", "spec document to store (- for stdin, the default)")
	dispatchSpecSeedCmd.Flags().BoolVar(&dispatchSeedOverwrite, "overwrite", false,
		"replace an existing spec memory with the shipped seed")

	dispatchProbeCmd.Flags().StringVar(&dispatchProbeModel, "model", "",
		"model to request and then confirm the provider reports (omit and nothing about model selection is verified)")
	dispatchProbeCmd.Flags().IntVar(&dispatchProbeTimeout, "timeout", engram.DefaultProbeTimeoutSeconds,
		"per-spawn timeout in seconds")
	dispatchProbeCmd.Flags().BoolVar(&dispatchProbeNoSave, "no-save", false,
		"report findings without writing them into the spec's provenance")
	dispatchProbeCmd.Flags().BoolVar(&dispatchProbeNoLive, "no-flag-liveness", false,
		"skip the invalid-model fallback probe")
	dispatchProbeCmd.Flags().BoolVar(&dispatchProbeKeepCtx, "keep-context", false,
		"let the child load its own project context (costs dollars rather than cents)")

	dispatchSurveyCmd.Flags().StringSliceVar(&dispatchSurveySubs, "subcommand", nil,
		"subcommand to walk help for (repeatable; default is guessed from the top-level help)")
	dispatchSurveyCmd.Flags().BoolVar(&dispatchSurveyJSON, "json", false, "output the survey as JSON")

	dispatchSpecCmd.AddCommand(dispatchSpecListCmd, dispatchSpecShowCmd, dispatchSpecValidateCmd,
		dispatchSpecPutCmd, dispatchSpecSeedCmd)
	dispatchCmd.AddCommand(dispatchRunCmd, dispatchSpecCmd, dispatchProbeCmd, dispatchSurveyCmd)
	markExperimental(dispatchCmd, "dispatch")
	rootCmd.AddCommand(dispatchCmd)
}
