#!/usr/bin/env bash
# Fast CI race lane. Full root-module and nested-module race sweeps run in
# scheduled-stress.yml; the ordinary CI suite still runs all tests on every PR.
set -euo pipefail

# Keep the session store, CLI, terminal host, and every serve subpackage intact.
# Only the main serve package needs a focused selection: it dominates race time.
serve_packages=$(go list ./internal/app/serve/...)
packages=(./internal/session ./internal/termhost ./cmd)
while IFS= read -r pkg; do
	case "$pkg" in
		*/internal/app/serve) ;;
		*) packages+=("$pkg") ;;
	esac
done <<< "$serve_packages"

# Select test families that run competing work or exercise process, session,
# and request lifetimes. Add new concurrency-focused families here as needed.
serve_tests=(
	'.*(Concurrent|Concurrency|Race)'
	'Agent(Barrier|Host|Wake)|Child(AskUser|Barrier|Lifecycle)|HostedChild|RunEnvironment|SpawnRunnerShutdown|SubagentDrain'
	'Hub(.*Reverse|Attention|Changes|Delegation|Health)|UploadWriterAdmission'
	'InterruptMessage|HandleResponse(ByID_Cancel|Cancel)|ResponseRun|Responses(Concurrent|Stream|Idempotency)|StartResponseRun'
	'Linked(Live|ToolContinuation)|Live(Client|Rebind)|TypedClientForeground'
	'Notification|Completion(Dispatch|Push)|ServeAutoTitle|ServeCompaction'
	'Serve(Reload|RuntimeClose|ServerStop|SessionManager|Shell|SideQuestion|SkillRunsCancel)|WebReload'
	'SessionInput|SynchronousHumanAdmission|MCPPolicy|ServeMCP|CodeMode|CommitOperation|FreshProviderRequest'
)
serve_pattern=$(IFS='|'; echo "^Test(${serve_tests[*]})")

scripts/gotest.sh -race -timeout=20m "$@" "${packages[@]}"
scripts/gotest.sh -race -timeout=20m "$@" -run "$serve_pattern" ./internal/app/serve
scripts/gotest.sh -race -tags browserfixture "$@" -run '^TestDebugBrowserGate$' ./internal/llm/debug
