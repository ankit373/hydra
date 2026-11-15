// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/recipe"
	"github.com/ankit373/hydra/internal/trust"
)

func cmdRecipe() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recipe",
		Short: "What hydra may try per kind of work: model, samples, checks and stop rule",
		Long: "A recipe is one way to get an answer: which model, how many answers to\n" +
			"sample, which checks judge them and when to stop. Each kind of work has a\n" +
			"menu of recipes in registry/recipes.yaml; a copy at $HYDRA_HOME/registry/\n" +
			"recipes.yaml wins. A kind with no menu gets one call only.",
	}
	cmd.AddCommand(cmdRecipeList())
	return cmd
}

func cmdRecipeList() *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Print each kind of work's menu of recipes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			book, err := recipe.Load(config.ScriptHome())
			if err != nil {
				return err
			}
			kinds := book.Kinds()
			if cmd.Flags().Changed("kind") {
				k, err := trust.ParseKind(kind)
				if err != nil {
					return err
				}
				kinds = []string{k}
			}
			fmt.Fprintln(stdout)
			for _, k := range kinds {
				printMenu(book, k)
			}
			if !cmd.Flags().Changed("kind") {
				fmt.Fprintf(stdout, "  %s\n\n", dimStyle.Render("every other kind of work: single"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "one kind of work, <kind>[.<area>]: "+strings.Join(trust.Kinds, ", "))
	return cmd
}

// printMenu prints kind's recipes in the order they are tried, as declared:
// a check is the kind of --check the request must bring.
func printMenu(book recipe.Book, kind string) {
	m := book.Menu(kind)
	fmt.Fprintf(stdout, "  %s  %s\n", cortexStyle.Render(kind), dimStyle.Render("default "+m.Default))
	for _, name := range m.Recipes {
		r, _ := book.Recipe(name)
		fmt.Fprintf(stdout, "    %-20s %s\n", name, dimStyle.Render(recipeLine(r)))
	}
	fmt.Fprintln(stdout)
}

func recipeLine(r recipe.Recipe) string {
	if r.Name == recipe.Single {
		return "one call to the ranked head · stops on the request's own checks, if any"
	}
	who := "the ranked head"
	switch {
	case r.Head != "":
		who = r.Head
	case r.Objective == rank.Quality:
		who = "the strongest measured head"
	case r.Objective == rank.Cost:
		who = "the cheapest measured head"
	}
	n := "1 answer"
	if r.Samples > 1 {
		n = fmt.Sprintf("up to %d answers", r.Samples)
	}
	parts := []string{who, n, "stop " + string(r.Stop)}
	if len(r.Checks) > 0 {
		parts = append(parts, "needs a "+strings.Join(r.Checks, " and a ")+" check")
	}
	if r.Fallback != "" {
		parts = append(parts, "then "+r.Fallback)
	}
	if r.MaxUSD > 0 {
		parts = append(parts, fmt.Sprintf("cap $%.4f", r.MaxUSD))
	}
	return strings.Join(parts, " · ")
}
