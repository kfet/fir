# bash completion for fir — also available via `fir completion bash`.
#
# Install:
#   fir completion bash > /etc/bash_completion.d/fir
# or for a single user:
#   fir completion bash > ~/.local/share/bash-completion/completions/fir

# Built-in slash commands, runnable as `fir /<command>`. Kept in sync with
# resources.BuiltinSlashCommands by TestCompletionScripts_SlashCommandsInSync.
_FIR_SLASH_COMMANDS="help theme thinking model settings session new compact resume tree export share name changelog login logout reload skills update reexec queue dequeue plan mcp quit"

_fir_complete() {
    local cur prev words cword
    if declare -F _init_completion >/dev/null 2>&1; then
        _init_completion -n = || return
    else
        COMPREPLY=()
        cur=${COMP_WORDS[COMP_CWORD]}
        prev=${COMP_WORDS[COMP_CWORD-1]}
        words=("${COMP_WORDS[@]}")
        cword=$COMP_CWORD
    fi

    local subcommand=""
    if [[ $cword -ge 2 ]]; then
        case ${words[1]} in
            update|skills|extensions|install|uninstall|packages|sessions|observe|htop|send|login|logout|auth|mcp|completion)
                subcommand=${words[1]}
                ;;
        esac
    fi

    # Subcommand-specific completion.
    # _fir_complete_subcommand reads cur/prev/cword/words from this scope
    # (bash dynamic scoping) — that's how _init_completion conventions work.
    if [[ -n $subcommand ]]; then
        _fir_complete_subcommand "$subcommand"
        return
    fi

    # Flag-value completion (top-level)
    case "$prev" in
        --mode)
            COMPREPLY=( $(compgen -W "text json acp" -- "$cur") ); return ;;
        --thinking)
            COMPREPLY=( $(compgen -W "off minimal low medium high xhigh max" -- "$cur") ); return ;;
        --provider|--login)
            COMPREPLY=( $(compgen -W "$(_fir_providers)" -- "$cur") ); return ;;
        --model)
            COMPREPLY=( $(compgen -W "$(_fir_models)" -- "$cur") ); return ;;
        -e|--extension|-d|--disable-extension)
            COMPREPLY=( $(compgen -W "$(_fir_extension_names)" -- "$cur") ); return ;;
        --tools)
            # comma-separated tool list; no built-in bash completion for that —
            # complete the last segment after the final comma.
            local last=${cur##*,}
            local prefix=${cur%"$last"}
            local opts="read bash edit write grep find ls"
            COMPREPLY=( $(compgen -W "$opts" -- "$last") )
            COMPREPLY=( "${COMPREPLY[@]/#/$prefix}" )
            return ;;
        --models)
            # comma-separated model patterns. Complete the last segment from the
            # live model registry, preserving everything before the trailing comma.
            local last=${cur##*,}
            local prefix=${cur%"$last"}
            COMPREPLY=( $(compgen -W "$(_fir_models)" -- "$last") )
            COMPREPLY=( "${COMPREPLY[@]/#/$prefix}" )
            return ;;
        --session)
            COMPREPLY=( $(compgen -W "$(_fir_session_names)" -- "$cur") ); return ;;
        -C|--cwd|--directory|--session-dir|--agent-dir)
            _filedir -d; return ;;
        --skill|--prompt-template|--theme|--mcp-config|--export|--debug-log-file|--append-system-prompt)
            _filedir; return ;;
        --api-key|--system-prompt|--session-name)
            return ;;
    esac

    # Flag completion
    if [[ $cur == -* ]]; then
        local flags="
            --help --version --print --continue --resume --no-restore-config
            --mode --thinking --agent-dir --provider --model --api-key
            --system-prompt --append-system-prompt
            --session --session-name --session-dir --no-session
            --models --no-tools --tools --no-mcp --mcp-config --wait-mcp --acp-session-idle-ttl
            --no-extensions --extension --disable-extension
            --skill --no-skills
            --prompt-template --no-prompt-templates
            --theme --no-themes
            --export --list-models --list-available-models
            --verbose --debug --debug-log-file
            --login -C --cwd --directory
            -h -V -v -vv -p -c -r -e -d
        "
        COMPREPLY=( $(compgen -W "$flags" -- "$cur") )
        return
    fi

    # First positional: subcommand or @file or message
    if [[ $cword -eq 1 ]]; then
        local subs="update skills extensions install uninstall packages sessions observe htop send login logout auth mcp completion"
        if [[ $cur == @* ]]; then
            local stripped=${cur#@}
            local files=( $(compgen -f -- "$stripped") )
            COMPREPLY=( "${files[@]/#/@}" )
            return
        fi
        if [[ $cur == /* ]]; then
            local names
            names="$(_fir_skill_names) $_FIR_SLASH_COMMANDS"
            local prefixed=()
            local n
            for n in $names; do prefixed+=("/$n"); done
            COMPREPLY=( $(compgen -W "${prefixed[*]}" -- "$cur") )
            return
        fi
        COMPREPLY=( $(compgen -W "$subs" -- "$cur") )
        return
    fi

    # Default: complete @file references
    if [[ $cur == @* ]]; then
        local stripped=${cur#@}
        local files=( $(compgen -f -- "$stripped") )
        COMPREPLY=( "${files[@]/#/@}" )
        return
    fi
}

_fir_complete_subcommand() {
    local sub=$1
    local pos=$((cword - 1))   # position within subcommand args (1-based)
    case "$sub" in
        skills)
            if [[ $pos -eq 1 ]]; then
                COMPREPLY=( $(compgen -W "list install" -- "$cur") )
            elif [[ ${words[2]} == install ]]; then
                if [[ $pos -eq 2 ]]; then
                    COMPREPLY=( $(compgen -W "$(_fir_skill_names)" -- "$cur") )
                else
                    COMPREPLY=( $(compgen -W "--user --force" -- "$cur") )
                fi
            fi ;;
        extensions)
            if [[ $pos -eq 1 ]]; then
                COMPREPLY=( $(compgen -W "list install" -- "$cur") )
            elif [[ ${words[2]} == install ]]; then
                if [[ $pos -eq 2 ]]; then
                    COMPREPLY=( $(compgen -W "$(_fir_extension_names)" -- "$cur") )
                else
                    COMPREPLY=( $(compgen -W "--user --force" -- "$cur") )
                fi
            fi ;;
        install)
            if [[ $cur == -* ]]; then
                COMPREPLY=( $(compgen -W "--local" -- "$cur") )
            else
                _filedir
            fi ;;
        uninstall)
            if [[ $cur == -* ]]; then
                COMPREPLY=( $(compgen -W "--local" -- "$cur") )
            else
                COMPREPLY=( $(compgen -W "$(_fir_packages_list)" -- "$cur") )
            fi ;;
        packages)
            if [[ $pos -eq 1 ]]; then
                COMPREPLY=( $(compgen -W "list update" -- "$cur") )
            elif [[ ${words[2]} == update && $pos -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$(_fir_packages_list)" -- "$cur") )
            fi ;;
        sessions)
            if [[ $pos -eq 1 ]]; then
                COMPREPLY=( $(compgen -W "list" -- "$cur") )
            fi ;;
        observe)
            COMPREPLY=( $(compgen -W "--json --full --cwd --interact" -- "$cur") ) ;;
        send)
            COMPREPLY=( $(compgen -W "--steer --follow --cwd" -- "$cur") ) ;;
        login)
            if [[ $pos -eq 1 ]]; then
                COMPREPLY=( $(compgen -W "list $(_fir_providers)" -- "$cur") )
            fi ;;
        auth)
            if [[ $pos -eq 1 ]]; then
                COMPREPLY=( $(compgen -W "refresh" -- "$cur") )
            else
                COMPREPLY=( $(compgen -W "--force --within --no-extensions --debug $(_fir_providers) $(_fir_slots)" -- "$cur") )
            fi ;;
        mcp)
            if [[ $pos -eq 1 ]]; then
                COMPREPLY=( $(compgen -W "list login logout" -- "$cur") )
            elif [[ $pos -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "$(_fir_mcp_servers)" -- "$cur") )
            fi ;;
        completion)
            if [[ $pos -eq 1 ]]; then
                COMPREPLY=( $(compgen -W "bash zsh" -- "$cur") )
            fi ;;
        update)
            : ;;
    esac
}

# --- Dynamic data sources (cached per shell) ---

_fir_mcp_servers() {
    if [[ -z ${_FIR_MCP_SERVERS_CACHE+x} ]]; then
        _FIR_MCP_SERVERS_CACHE=$(fir mcp list 2>/dev/null | awk 'NR>1 && $1 != "" {print $1}')
    fi
    printf '%s\n' "$_FIR_MCP_SERVERS_CACHE"
}

_fir_models() {
    if [[ -z ${_FIR_MODELS_CACHE+x} ]]; then
        _FIR_MODELS_CACHE=$(fir --list-models 2>/dev/null)
    fi
    printf '%s\n' "$_FIR_MODELS_CACHE"
}

_fir_providers() {
    local builtin="anthropic openai google bedrock azure-openai openai-codex google-vertex google-gemini-cli poe groq cerebras deepseek xai openrouter github-copilot openai-antigravity"
    if [[ -z ${_FIR_PROVIDERS_CACHE+x} ]]; then
        _FIR_PROVIDERS_CACHE=$(fir --list-models 2>/dev/null | awk -F/ '{print $1}' | sort -u)
    fi
    printf '%s\n%s\n' "$builtin" "$_FIR_PROVIDERS_CACHE"
}

# Stored account slot keys ("anthropic", "anthropic#work@x.com") as printed by
# `fir login list` — what `fir auth refresh` and `fir logout` accept.
_fir_slots() {
    if [[ -z ${_FIR_SLOTS_CACHE+x} ]]; then
        _FIR_SLOTS_CACHE=$(fir login list 2>/dev/null | awk '$2 ~ /^\[/ {print $1}')
    fi
    printf '%s\n' "$_FIR_SLOTS_CACHE"
}

_fir_extension_names() {
    if [[ -z ${_FIR_EXT_CACHE+x} ]]; then
        _FIR_EXT_CACHE=$(fir extensions list 2>/dev/null | awk 'NR>1 && $1!="NAME" {print $1}')
    fi
    printf '%s\n' "$_FIR_EXT_CACHE"
}

_fir_skill_names() {
    if [[ -z ${_FIR_SKILL_CACHE+x} ]]; then
        _FIR_SKILL_CACHE=$(fir skills list 2>/dev/null | awk 'NR>1 && $1!="NAME" {print $1}')
    fi
    printf '%s\n' "$_FIR_SKILL_CACHE"
}

_fir_session_names() {
    if [[ -z ${_FIR_SESSION_CACHE+x} ]]; then
        _FIR_SESSION_CACHE=$(fir sessions list 2>/dev/null | awk 'NR>1 && $1!="ID" {print $1}')
    fi
    printf '%s\n' "$_FIR_SESSION_CACHE"
}

_fir_packages_list() {
    if [[ -z ${_FIR_PKG_CACHE+x} ]]; then
        _FIR_PKG_CACHE=$(fir packages list 2>/dev/null | awk 'NR>1 && $1!="SOURCE" {print $1}')
    fi
    printf '%s\n' "$_FIR_PKG_CACHE"
}

complete -F _fir_complete fir
