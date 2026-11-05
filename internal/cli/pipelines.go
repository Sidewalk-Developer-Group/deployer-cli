package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
	"github.com/sidewalk-developer-group/deployer-cli/internal/ui"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// exportPipeline writes the original bytes so export/import is lossless.
func exportPipeline(ctx context.Context, c *api.Client, printer *ui.Printer, env, id, file string, out io.Writer) error {
	pipelines, err := c.Pipelines(ctx, env)
	if err != nil {
		return err
	}
	selected := ""
	for _, p := range pipelines {
		if (id != "" && p.ID == id) || (id == "" && p.IsActive) {
			selected = p.ID
			break
		}
	}
	if selected == "" {
		if id != "" {
			return exit.Errorf(exit.Usage, "pipeline %s does not belong to this environment", id)
		}
		return exit.Errorf(exit.Usage, "environment has no active pipeline; pass --pipeline <id>")
	}
	b, err := c.ExportPipeline(ctx, selected)
	if err != nil {
		return err
	}
	if file == "" {
		_, err = out.Write(b)
		return err
	}
	if err = os.WriteFile(file, b, 0644); err != nil {
		return err
	}
	side := *printer
	side.Out = printer.Err
	side.Success("exported pipeline to %s", file)
	return nil
}

type pipelineImportOptions struct{ yes, activate, json bool }

// importPipeline parses only the review summary; the submitted document stays untouched.
func importPipeline(ctx context.Context, c *api.Client, printer *ui.Printer, p *prompter, env, target string, document []byte, o pipelineImportOptions) error {
	if len(document) > 262144 {
		return exit.Errorf(exit.Usage, "pipeline YAML exceeds 262144 bytes")
	}
	if !o.yes && p == nil {
		return exit.Errorf(exit.Usage, "pass --yes to import without confirmation")
	}
	var summary struct {
		Name   string `yaml:"name"`
		Stages []struct {
			Name string `yaml:"name"`
			Type string `yaml:"type"`
		} `yaml:"stages"`
	}
	if err := yaml.Unmarshal(document, &summary); err != nil {
		return exit.With(exit.Usage, fmt.Errorf("invalid pipeline YAML: %w", err))
	}
	side := *printer
	side.Out = printer.Err
	side.Printf("Pipeline: %s\nTarget: %s\n", summary.Name, target)
	for i, stage := range summary.Stages {
		side.Printf("%d. %s (%s)\n", i+1, stage.Name, stage.Type)
	}
	if !o.yes {
		ok, err := p.confirm("Import into " + target + "?")
		if err != nil {
			return err
		}
		if !ok {
			side.Printf("aborted, nothing imported\n")
			return nil
		}
	}
	pipeline, warnings, err := c.ImportPipeline(ctx, env, document)
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		printer.Warn("%s", warning)
	}
	state := "inactive"
	if o.activate {
		imported := pipeline
		pipeline, err = c.ActivatePipeline(ctx, pipeline.ID)
		if err != nil {
			return fmt.Errorf("imported pipeline %s (%s), inactive; activation failed: %w", summary.Name, imported.ID, err)
		}
		state = "active"
	}
	side.Success("imported pipeline %s (%s), %s", pipeline.Name, pipeline.ID, state)
	if !o.activate {
		side.Dim("activate it in the web app or re-run with --activate")
	}
	if o.json {
		return printJSON(printer, map[string]any{"pipeline": pipeline, "warnings": warnings})
	}
	return nil
}

func listPipelines(printer *ui.Printer, pipelines []api.Pipeline, asJSON bool) error {
	if asJSON {
		return printJSON(printer, pipelines)
	}
	rows := make([][]string, 0, len(pipelines))
	for _, p := range pipelines {
		active := ""
		if p.IsActive {
			active = "✓"
		}
		stages := "—"
		if p.StagesCount != nil {
			stages = strconv.Itoa(*p.StagesCount)
		}
		rows = append(rows, []string{p.ID, p.Name, active, strconv.Itoa(p.Version), stages, ui.Ago(p.UpdatedAt)})
	}
	printer.Table([]string{"ID", "NAME", "ACTIVE", "VERSION", "STAGES", "UPDATED"}, rows)
	return nil
}

func newPipelinesCommand(f *Factory) *cobra.Command {
	root := &cobra.Command{
		Use:     "pipelines",
		Aliases: []string{"pipeline"},
		Short:   "List, export and import environment pipelines",
		Long: "Manage pipeline versions for a project and environment, using their slugs.\n" +
			"Export unchanged YAML for review or import it into another environment.",
		Example: "  deployer pipelines ls billing-api production\n" +
			"  deployer pipelines export billing-api production -o pipeline.yml\n" +
			"  deployer pipelines import billing-api staging -f pipeline.yml",
		Args: cobra.NoArgs,
	}
	var output, id, input string
	var o pipelineImportOptions
	resolve := func(cmd *cobra.Command, args []string) (*api.Client, string, error) {
		s, err := f.session()
		if err != nil {
			return nil, "", err
		}
		s.warnFileToken(f.Printer())
		org, err := f.organization(cmd.Context(), s)
		if err != nil {
			return nil, "", err
		}
		target, err := s.Client.Resolve(cmd.Context(), org, args[0], args[1])
		if err != nil {
			return nil, "", explainResolveFailure(f.Printer(), err, args[0], args[1])
		}
		if target.Environment == nil {
			return nil, "", exit.Errorf(exit.Usage, "environment %s was not resolved", args[1])
		}
		return s.Client, target.Environment.ID, nil
	}
	list := &cobra.Command{
		Use:     "ls <project> <env>",
		Aliases: []string{"list"},
		Short:   "List environment pipelines",
		Long: "Lists pipeline versions, including their IDs, active state, stage count and update time.\n" +
			"Use --json for the full response or --pipeline with export to select a version.",
		Example: "  deployer pipelines ls billing-api production\n" +
			"  deployer pipelines ls billing-api staging --json",
		Args: wantArgs(2, 2, "deployer pipelines ls <project> <env>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, env, err := resolve(cmd, args)
			if err != nil {
				return err
			}
			pipelines, err := c.Pipelines(cmd.Context(), env)
			if err != nil {
				return err
			}
			return listPipelines(f.Printer(), pipelines, f.JSON)
		},
	}
	export := &cobra.Command{
		Use:   "export <project> <env>",
		Short: "Export the active pipeline as YAML",
		Long: "Writes the server's YAML unchanged to stdout, or to a file with -o. Exports\n" +
			"contain no secret values. Defaults to the active pipeline; --pipeline selects\n" +
			"a version by ID from `deployer pipelines ls`.",
		Example: "  deployer pipelines export billing-api production -o pipeline.yml\n" +
			"  deployer pipelines export billing-api production | deployer pipelines import billing-api staging -f - --yes",
		Args: wantArgs(2, 2, "deployer pipelines export <project> <env>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, env, err := resolve(cmd, args)
			if err != nil {
				return err
			}
			return exportPipeline(cmd.Context(), c, f.Printer(), env, id, output, cmd.OutOrStdout())
		},
	}
	export.Flags().StringVarP(&output, "output", "o", "", "write unchanged YAML to a file")
	export.Flags().StringVar(&id, "pipeline", "", "export this pipeline from the environment")
	imp := &cobra.Command{
		Use:   "import <project> <env> -f <file|->",
		Short: "Import pipeline YAML as an inactive version",
		Long: "Creates an INACTIVE pipeline from YAML; --activate activates it after import.\n" +
			"Shows the pipeline name, target and stages, then asks for confirmation (y/N).\n" +
			"--yes skips confirmation and is required for -f - or noninteractive imports.\n" +
			"Documents are limited to 256 KiB; summaries and warnings go to stderr.",
		Example: "  deployer pipelines import billing-api staging -f pipeline.yml\n" +
			"  deployer pipelines import billing-api staging -f pipeline.yml --activate --yes\n" +
			"  deployer pipelines export billing-api production | deployer pipelines import billing-api staging -f - --yes",
		Args: wantArgs(2, 2, "deployer pipelines import <project> <env> -f <file|->"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if input == "" {
				return exit.Errorf(exit.Usage, "pass -f <file|->")
			}
			p := commandPrompter(cmd, f.Printer(), o.yes, false)
			if !o.yes && (input == "-" || p == nil) {
				return exit.Errorf(exit.Usage, "pass --yes to import without confirmation")
			}
			var reader io.Reader = cmd.InOrStdin()
			if input != "-" {
				file, err := os.Open(input)
				if err != nil {
					return err
				}
				defer file.Close()
				reader = file
			}
			document, err := io.ReadAll(io.LimitReader(reader, 262145))
			if err != nil {
				return err
			}
			if len(document) > 262144 {
				return exit.Errorf(exit.Usage, "pipeline YAML exceeds 262144 bytes")
			}
			c, env, err := resolve(cmd, args)
			if err != nil {
				return err
			}
			o.json = f.JSON
			return importPipeline(cmd.Context(), c, f.Printer(), p, env, args[0]+"/"+args[1], document, o)
		},
	}
	imp.Flags().StringVarP(&input, "file", "f", "", "YAML file, or - for stdin")
	imp.Flags().BoolVarP(&o.yes, "yes", "y", false, "import without confirmation")
	imp.Flags().BoolVar(&o.activate, "activate", false, "activate the imported pipeline")
	root.AddCommand(list, export, imp)
	return root
}
