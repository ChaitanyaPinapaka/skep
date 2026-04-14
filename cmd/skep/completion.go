package main

import (
	"fmt"
	"strings"
)

// cmdCompletion prints a static shell completion script for bash, zsh,
// or fish. Static means verbs and common flags — no live task-id or
// symbol completion, which would require opening .skep/index.db.
func cmdCompletion(args []string) error {
	if len(args) == 0 {
		return usageErrorf("usage: skep completion <bash|zsh|fish>")
	}
	switch strings.ToLower(args[0]) {
	case "bash":
		fmt.Print(bashCompletionScript())
	case "zsh":
		fmt.Print(zshCompletionScript())
	case "fish":
		fmt.Print(fishCompletionScript())
	default:
		return usageErrorf("completion: unknown shell %q (supported: bash, zsh, fish)", args[0])
	}
	return nil
}

// Sub-verb lists. Kept in one place so every shell's script sees the
// same source of truth.
const (
	completionTaskVerbs      = "create list ls show run attach approve reject done delete rm clarify jump-pending"
	completionWorkspaceVerbs = "list watch oneline status"
	completionIndexVerbs     = "create build refresh ask search"
	completionDaemonVerbs    = "start stop status"
	completionCockpitVerbs   = "setup reset status"
)

// completionTopLevelVerbs is the static list of top-level verbs exposed
// by completion scripts. Declared independently of the `commands` table
// to avoid an initialization cycle (commands → cmdCompletion →
// script → commands). Keep this list in sync when adding a new verb.
const completionTopLevelVerbs = "init index status task tasks workspace daemon mcp doctor cockpit completion help version"

func topLevelCmdNames() string { return completionTopLevelVerbs }

func bashCompletionScript() string {
	return `# bash completion for skep
_skep() {
	local cur prev words cword
	_init_completion || return

	local top_cmds="` + topLevelCmdNames() + `"
	local task_verbs="` + completionTaskVerbs + `"
	local workspace_verbs="` + completionWorkspaceVerbs + `"
	local index_verbs="` + completionIndexVerbs + `"
	local daemon_verbs="` + completionDaemonVerbs + `"
	local cockpit_verbs="` + completionCockpitVerbs + `"
	local shells="bash zsh fish"

	if [[ ${cword} -eq 1 ]]; then
		COMPREPLY=( $(compgen -W "${top_cmds}" -- "${cur}") )
		return
	fi

	local cmd="${words[1]}"
	case "${cmd}" in
		task|tasks)
			if [[ ${cword} -eq 2 ]]; then
				COMPREPLY=( $(compgen -W "${task_verbs}" -- "${cur}") )
				return
			fi
			COMPREPLY=( $(compgen -W "--json --dry-run --all --yes" -- "${cur}") )
			;;
		workspace)
			[[ ${cword} -eq 2 ]] && COMPREPLY=( $(compgen -W "${workspace_verbs}" -- "${cur}") )
			;;
		index)
			if [[ ${cword} -eq 2 ]]; then
				COMPREPLY=( $(compgen -W "${index_verbs}" -- "${cur}") )
			else
				COMPREPLY=( $(compgen -W "--json" -- "${cur}") )
			fi
			;;
		daemon)
			[[ ${cword} -eq 2 ]] && COMPREPLY=( $(compgen -W "${daemon_verbs}" -- "${cur}") )
			;;
		cockpit)
			[[ ${cword} -eq 2 ]] && COMPREPLY=( $(compgen -W "${cockpit_verbs}" -- "${cur}") )
			;;
		completion)
			[[ ${cword} -eq 2 ]] && COMPREPLY=( $(compgen -W "${shells}" -- "${cur}") )
			;;
		status|doctor)
			COMPREPLY=( $(compgen -W "--json --oneline --watch" -- "${cur}") )
			;;
		help)
			[[ ${cword} -eq 2 ]] && COMPREPLY=( $(compgen -W "${top_cmds}" -- "${cur}") )
			;;
	esac
}
complete -F _skep skep
`
}

func zshCompletionScript() string {
	return `#compdef skep
# zsh completion for skep

_skep() {
	local -a top_cmds task_verbs workspace_verbs index_verbs daemon_verbs cockpit_verbs shells
	top_cmds=(` + zshWords(topLevelCmdNames()) + `)
	task_verbs=(` + zshWords(completionTaskVerbs) + `)
	workspace_verbs=(` + zshWords(completionWorkspaceVerbs) + `)
	index_verbs=(` + zshWords(completionIndexVerbs) + `)
	daemon_verbs=(` + zshWords(completionDaemonVerbs) + `)
	cockpit_verbs=(` + zshWords(completionCockpitVerbs) + `)
	shells=('bash' 'zsh' 'fish')

	if (( CURRENT == 2 )); then
		_describe 'skep command' top_cmds
		return
	fi

	case "${words[2]}" in
		task|tasks)
			if (( CURRENT == 3 )); then
				_describe 'task verb' task_verbs
			else
				_values 'option' '--json' '--dry-run' '--all' '--yes'
			fi
			;;
		workspace)
			(( CURRENT == 3 )) && _describe 'workspace verb' workspace_verbs
			;;
		index)
			if (( CURRENT == 3 )); then
				_describe 'index verb' index_verbs
			else
				_values 'option' '--json'
			fi
			;;
		daemon)
			(( CURRENT == 3 )) && _describe 'daemon verb' daemon_verbs
			;;
		cockpit)
			(( CURRENT == 3 )) && _describe 'cockpit verb' cockpit_verbs
			;;
		completion)
			(( CURRENT == 3 )) && _describe 'shell' shells
			;;
		status|doctor)
			_values 'option' '--json' '--oneline' '--watch'
			;;
		help)
			(( CURRENT == 3 )) && _describe 'help topic' top_cmds
			;;
	esac
}

_skep "$@"
`
}

func fishCompletionScript() string {
	top := topLevelCmdNames()
	return `# fish completion for skep
function __skep_using_command
	set -l cmd (commandline -opc)
	test (count $cmd) -gt 1; and test "$cmd[2]" = "$argv[1]"
end

complete -c skep -n 'not __fish_seen_subcommand_from ` + top + `' -f -a '` + top + `'
complete -c skep -n '__skep_using_command task'       -f -a '` + completionTaskVerbs + `'
complete -c skep -n '__skep_using_command tasks'      -f -a '` + completionTaskVerbs + `'
complete -c skep -n '__skep_using_command workspace'  -f -a '` + completionWorkspaceVerbs + `'
complete -c skep -n '__skep_using_command index'      -f -a '` + completionIndexVerbs + `'
complete -c skep -n '__skep_using_command daemon'     -f -a '` + completionDaemonVerbs + `'
complete -c skep -n '__skep_using_command cockpit'    -f -a '` + completionCockpitVerbs + `'
complete -c skep -n '__skep_using_command completion' -f -a 'bash zsh fish'

complete -c skep -l json    -d 'Machine-readable JSON output'
complete -c skep -l dry-run -d "Don't persist; just show what would happen"
complete -c skep -l all     -d 'Show across all repos in workspace'
complete -c skep -l yes -s y -d 'Skip confirmation prompts'
`
}

// zshWords quotes each whitespace-separated word for a zsh array
// literal.
func zshWords(s string) string {
	parts := strings.Fields(s)
	var b strings.Builder
	for _, p := range parts {
		fmt.Fprintf(&b, " '%s'", p)
	}
	return b.String()
}
