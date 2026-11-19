package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/term-llm/term-llm/internal/app/proxyconnect"
	"github.com/term-llm/term-llm/internal/app/proxyrun"
	"github.com/term-llm/term-llm/internal/exitcode"
)

func init() { rootCmd.AddCommand(newProxyCommand()) }

func newProxyCommand() *cobra.Command {
	root := &cobra.Command{
		Use: "proxy", Short: "Run native clients with credentials held by a proxy",
		PersistentPreRunE: proxyPreRun, Annotations: map[string]string{"isolated-startup": "true"},
	}
	run := &cobra.Command{Use: "run", Short: "Run a native client with process-local proxy settings"}
	run.AddCommand(newProxyRunClient("claude", "Claude Code", "Claude", "claude-bin", `Run Claude Code with a process-local proxy environment and an isolated
term-managed Claude profile. Your normal ~/.claude login and settings are untouched.

Put every native Claude argument after -- so term-llm flags cannot consume it:
  term-llm proxy run claude -- --resume
  term-llm proxy run claude -- -p "summarize this repo"`, proxyrun.Run))
	run.AddCommand(newProxyRunClient("agy", "Antigravity", "Antigravity", "agy-bin", `Sign in to a personal Antigravity subscription through the scoped OAuth proxy.
Requires agy 1.5.12 or newer on Linux. The browser stays outside the proxy; follow
agy's instructions and paste the result into its terminal prompt. Your normal
Antigravity login and settings are not used. Put native arguments after --:
  term-llm proxy run agy -- acp --login`, proxyrun.RunAgy))
	disconnect := &cobra.Command{Use: "disconnect", Short: "Undo a legacy connector's Claude-only gateway settings"}
	undo := &cobra.Command{
		Use: "claude", Short: "Restore legacy connector-owned Claude settings without overwriting unrelated settings",
		Args: cobra.NoArgs, Annotations: map[string]string{"isolated-startup": "true"},
		RunE: func(c *cobra.Command, _ []string) error {
			return proxyconnect.Disconnect(proxyconnect.Options{Out: c.OutOrStdout()})
		},
	}
	disconnect.AddCommand(undo)
	root.AddCommand(run, disconnect)
	return root
}

func newProxyRunClient(executable, display, subject, provider, long string, run func(context.Context, proxyrun.Options) error) *cobra.Command {
	var opts proxyrun.Options
	client := &cobra.Command{
		Use: executable + " [-- native args...]", Short: "Run " + display + " behind the credential proxy", Long: long,
		Args: cobra.ArbitraryArgs, Annotations: map[string]string{"isolated-startup": "true"},
		RunE: func(c *cobra.Command, args []string) error {
			opts.Args = args
			opts.In, opts.Out, opts.Err = c.InOrStdin(), c.OutOrStdout(), c.ErrOrStderr()
			err := run(c.Context(), opts)
			var childExit *proxyrun.ChildExitError
			if errors.As(err, &childExit) {
				message := fmt.Sprintf("%s exited with status %d", executable, childExit.Code)
				if childExit.Code == exitcode.Cancelled {
					message = executable + " interrupted"
				}
				return exitcode.ExitError{Code: childExit.Code, Message: message}
			}
			return err
		},
	}
	client.Flags().StringVar(&opts.URL, "url", "", "Exact scoped "+subject+" proxy binding root (default: configured "+provider+" binding)")
	client.Flags().StringVar(&opts.Token, "token", "", "Scoped proxy client token for --url (never a "+subject+" vendor credential)")
	client.Flags().StringVar(&opts.Command, executable+"-command", executable, display+" executable to launch")
	return client
}
