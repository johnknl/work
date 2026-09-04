package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

type config struct {
	worktreeDir    string
	worktreePrefix string
	printPath      bool
	forceCreate    bool
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		exitErr(err)
	}

	root := &cobra.Command{
		Use:   "work [name]",
		Short: "Open and manage git worktrees",
		Long:  "Open/switch named worktrees. WORKTREE_DIR defaults to $PWD/.work.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return openOrCreateWorktree(cfg, args[0])
		},
		ValidArgsFunction: worktreeNameCompletion,
	}

	root.Flags().BoolVarP(&cfg.forceCreate, "force", "f", false, "skip create confirmation")
	root.Flags().BoolVarP(&cfg.printPath, "print", "p", false, "print path")

	root.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List existing worktrees",
		RunE: func(_ *cobra.Command, _ []string) error {
			return listWorktrees(cfg)
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "prune",
		Short: "Prune stale worktree metadata",
		RunE: func(_ *cobra.Command, _ []string) error {
			return run("git", "worktree", "prune", "--verbose")
		},
	})

	removeCmd := &cobra.Command{
		Use:               "remove <name>",
		Short:             "Remove named worktree and its branch",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: worktreeNameCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			forceRemove, err := cmd.Flags().GetBool("force")
			if err != nil {
				return err
			}
			return removeWorktree(cfg, args[0], forceRemove)
		},
	}
	removeCmd.Flags().BoolP("force", "f", false, "skip confirmation")
	root.AddCommand(removeCmd)

	root.CompletionOptions.HiddenDefaultCmd = true
	root.InitDefaultCompletionCmd()

	exitErr(root.Execute())
}

func loadConfig() (config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return config{}, fmt.Errorf("get working directory: %w", err)
	}

	repoRoot := cwd
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err == nil {
		repoRoot = strings.TrimSpace(string(out))
	}

	worktreeDir := os.Getenv("WORKTREE_DIR")
	if worktreeDir == "" {
		worktreeDir = filepath.Join(cwd, ".work")
	}

	return config{
		worktreeDir:    worktreeDir,
		worktreePrefix: filepath.Base(repoRoot) + "-",
		printPath:      false,
		forceCreate:    false,
	}, nil
}

func listWorktrees(cfg config) error {
	names, err := worktreeNames(cfg.worktreePrefix)
	if err != nil {
		return err
	}

	for _, name := range names {
		fmt.Println(name)
	}

	return nil
}

func worktreeNames(prefix string) ([]string, error) {
	out, err := exec.Command("git", "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}

	var names []string
	for _, line := range bytes.Split(out, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("worktree ")) {
			continue
		}
		path := strings.TrimPrefix(string(line), "worktree ")
		base := filepath.Base(path)
		if strings.HasPrefix(base, prefix) {
			names = append(names, strings.TrimPrefix(base, prefix))
		}
	}

	return names, nil
}

func worktreeNameCompletion(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	names, err := worktreeNames(cfg.worktreePrefix)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	completions := make([]string, 0, len(names))
	for _, name := range names {
		if strings.HasPrefix(name, toComplete) {
			completions = append(completions, name)
		}
	}

	return completions, cobra.ShellCompDirectiveNoFileComp
}

func removeWorktree(cfg config, name string, forceRemove bool) error {
	branch := "feat/" + name
	targetPath := filepath.Join(cfg.worktreeDir, cfg.worktreePrefix+name)

	if !exists(targetPath) {
		return fmt.Errorf("worktree does not exist: %s", targetPath)
	}

	if forceRemove {
		fmt.Printf("Force removing worktree and branch for '%s'\n", name)
	} else {
		ok, err := confirm(fmt.Sprintf("Are you sure you want to remove the worktree and branch for '%s'? [y/N] ", name))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("aborting")
		}
	}

	if err := run("git", "worktree", "remove", targetPath); err != nil {
		return err
	}

	return run("git", "branch", "-d", branch)
}

func openOrCreateWorktree(cfg config, name string) error {
	branch := "feat/" + name
	targetPath := filepath.Join(cfg.worktreeDir, cfg.worktreePrefix+name)

	if err := os.MkdirAll(cfg.worktreeDir, 0o755); err != nil {
		return fmt.Errorf("create worktree root: %w", err)
	}

	if !exists(targetPath) {
		if !cfg.forceCreate {
			ok, err := confirm(fmt.Sprintf("Worktree '%s' does not exist. Create it? [y/N] ", name))
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("aborting worktree creation")
			}
		}

		if branchExists(branch) {
			if err := run("git", "worktree", "add", targetPath, branch); err != nil {
				return err
			}
		} else {
			if err := run("git", "worktree", "add", "-b", branch, targetPath); err != nil {
				return err
			}
		}
	}

	if cfg.printPath {
		fmt.Println(targetPath)
		return nil
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		return run("tmux", "new-window", "-c", targetPath)
	}

	return run("tmux", "new-window", "-c", targetPath, editor)
}

func branchExists(branch string) bool {
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

func confirm(prompt string) (bool, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	text, err := reader.ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("read input: %w", err)
	}
	return strings.TrimSpace(text) == "y", nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run %s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func exitErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
