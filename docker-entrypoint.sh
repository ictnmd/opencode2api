#!/bin/sh
set -eu

state_dir=${STATE_DIR:-/var/lib/opencode2api}
config_path=${CONFIG_PATH:-$state_dir/config.json}
config_seed_path=${CONFIG_SEED_PATH:-}
# Empty means "defer to config.json". Only an explicitly provided value becomes
# a command-line override, so the file stays authoritative as documented.
listen_address=${LISTEN_ADDRESS:-}
webui_listen_address=${WEBUI_LISTEN_ADDRESS:-}

if [ "$(id -u)" = "0" ]; then
    mkdir -p "$state_dir"
    chown opencode2api:opencode2api "$state_dir"
    exec su-exec opencode2api:opencode2api "$0" "$@"
fi

if [ "$#" -gt 0 ]; then
    exec "$@"
fi

warn() { printf '%s\n' "o2a: $*" >&2; }
note() { printf '%s\n' "o2a: $*"; }

# The example config binds the API to loopback, which is unreachable from
# outside a container through published ports. Rewrite only that exact default
# while seeding; any other address is left exactly as the operator wrote it.
seed_container_listen() {
    if grep -q '"listen"[[:space:]]*:[[:space:]]*"127.0.0.1:8080"' "$1"; then
        sed -i 's|"listen"[[:space:]]*:[[:space:]]*"127.0.0.1:8080"|"listen": "0.0.0.0:8080"|' "$1"
        printf '%s\n' "Seeded $1 with listen 0.0.0.0:8080 so it is reachable through published ports."
    fi
}

if [ ! -f "$config_path" ]; then
    mkdir -p "$(dirname "$config_path")"
    if [ -n "$config_seed_path" ] && [ -f "$config_seed_path" ]; then
        cp "$config_seed_path" "$config_path"
        seed_container_listen "$config_path"
        printf '%s\n' "Initialized $config_path from $config_seed_path."
    else
        cp /app/config.example.json "$config_path"
        seed_container_listen "$config_path"
        printf '%s\n' \
            "config.json not found; created $config_path. Set API keys or enable anonymous mode, and change the WebUI password before use."
    fi
fi

# The runtime config holds upstream keys and proxy credentials. Keep it private
# to the service user regardless of how the seed file was mounted.
chmod 600 "$config_path" 2>/dev/null || true

# ---------------------------------------------------------------------------
# O2A_* environment overlay (opt-in).
#
# Priority per field:  VAR_FILE  >  VAR  >  value already in config.json
#
# Unset or empty variables never change the file, so a config managed through
# the WebUI keeps working when no O2A_* variable is exported. The overlay is
# applied on every start (not only while seeding), which means an exported
# variable keeps winning over a later WebUI edit. See docs/env-overlay.md.
# ---------------------------------------------------------------------------

# Mask URL userinfo (https://user:pass@host -> https://***@host) before a value
# is written to the log. Upstream and proxy URLs may carry credentials.
o2a_redact() {
    printf '%s' "$1" | sed 's|://[^/@]*@|://***@|'
}

# o2a_read NAME: print the resolved value for NAME, honoring NAME_FILE first.
# Returns 1 when the variable resolves to nothing.
o2a_read() {
    o2a_name=$1
    eval "o2a_file=\${${o2a_name}_FILE:-}"
    if [ -n "$o2a_file" ]; then
        if [ -f "$o2a_file" ]; then
            printf '%s' "$(tr -d '\r' <"$o2a_file")"
            return 0
        fi
        warn "${o2a_name}_FILE=$o2a_file is not readable; falling back to $o2a_name"
    fi
    eval "o2a_value=\${$o2a_name:-}"
    if [ -n "$o2a_value" ]; then
        printf '%s' "$o2a_value"
        return 0
    fi
    return 1
}

# Split a comma- or newline-separated list into one trimmed entry per line and
# drop comments (#, ; and // at the start of an entry or after whitespace), so
# the same file format as config.proxyfile works here.
#
# The substitutions stay deliberately alternation-free: the image ships busybox
# sed, which does not implement GNU's "\|" operator. Whole-line comments are
# removed before inline ones so that a leading marker cannot survive.
o2a_lines() {
    tr ',' '\n' |
        sed 's/\r$//' |
        sed 's|^[[:space:]]*//.*$||' |
        sed 's|^[[:space:]]*[#;].*$||' |
        sed 's|[[:space:]]//.*$||' |
        sed 's|[[:space:]][#;].*$||' |
        sed 's/^[[:space:]]*//;s/[[:space:]]*$//' |
        sed '/^$/d'
}

o2a_bool() {
    case "$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')" in
    true | 1 | yes | y | on) printf 'true' ;;
    false | 0 | no | n | off) printf 'false' ;;
    *) return 1 ;;
    esac
}

o2a_int() {
    case "$1" in
    '' | *[!0-9]*) return 1 ;;
    *) printf '%s' "$1" ;;
    esac
}

overlay_config() {
    if ! command -v jq >/dev/null 2>&1; then
        warn "jq not found; skipping the O2A_* environment overlay"
        return 0
    fi

    o2a_dir=$(dirname "$config_path")
    o2a_work=$(mktemp -d "$o2a_dir/.o2a-overlay.XXXXXX" 2>/dev/null || mktemp -d) || {
        warn "cannot create a temporary directory; skipping the O2A_* environment overlay"
        return 0
    }
    o2a_patch=$o2a_work/patch.json
    printf '%s' '{}' >"$o2a_patch"

    # patch_json PATH JSON — set PATH to an already-encoded JSON value.
    patch_json() {
        if jq --argjson v "$2" "$1 = \$v" "$o2a_patch" >"$o2a_work/next.json" 2>"$o2a_work/jq.err"; then
            mv "$o2a_work/next.json" "$o2a_patch"
        else
            warn "ignoring override for $1: $(cat "$o2a_work/jq.err")"
            rm -f "$o2a_work/next.json"
        fi
    }

    # patch_str PATH VALUE — set PATH to a JSON string.
    patch_str() {
        if jq --arg v "$2" "$1 = \$v" "$o2a_patch" >"$o2a_work/next.json" 2>"$o2a_work/jq.err"; then
            mv "$o2a_work/next.json" "$o2a_patch"
        else
            warn "ignoring override for $1: $(cat "$o2a_work/jq.err")"
            rm -f "$o2a_work/next.json"
        fi
    }

    apply_str() { # VAR PATH LABEL
        if o2a_raw=$(o2a_read "$1"); then
            patch_str "$2" "$o2a_raw"
            note "override $3 = $(o2a_redact "$o2a_raw")"
        fi
    }

    apply_list() { # VAR PATH LABEL
        if o2a_raw=$(o2a_read "$1"); then
            o2a_list=$(printf '%s' "$o2a_raw" | o2a_lines)
            if [ -z "$o2a_list" ]; then
                warn "ignoring $1: no usable entries after splitting"
                return 0
            fi
            o2a_json=$(printf '%s\n' "$o2a_list" | jq -R -s 'split("\n") | map(select(length > 0))')
            patch_json "$2" "$o2a_json"
            note "override $3 ($(printf '%s\n' "$o2a_list" | wc -l | tr -d ' ') entries)"
        fi
    }

    apply_bool() { # VAR PATH LABEL
        if o2a_raw=$(o2a_read "$1"); then
            if o2a_json=$(o2a_bool "$o2a_raw"); then
                patch_json "$2" "$o2a_json"
                note "override $3 = $o2a_json"
            else
                warn "ignoring $1=$o2a_raw (expected true or false)"
            fi
        fi
    }

    apply_int() { # VAR PATH LABEL
        if o2a_raw=$(o2a_read "$1"); then
            if o2a_json=$(o2a_int "$o2a_raw"); then
                patch_json "$2" "$o2a_json"
                note "override $3 = $o2a_json"
            else
                warn "ignoring $1=$o2a_raw (expected a non-negative integer)"
            fi
        fi
    }

    # apply_json_object VAR PATH LABEL — the variable must already hold a JSON
    # object. Validated here so a typo cannot replace the mapping with a string
    # that Go would then reject at startup.
    apply_json_object() { # VAR PATH LABEL
        if o2a_raw=$(o2a_read "$1"); then
            if printf '%s' "$o2a_raw" | jq -e 'type == "object"' >/dev/null 2>&1; then
                patch_json "$2" "$o2a_raw"
                note "override $3 ($(printf '%s' "$o2a_raw" | jq 'length' 2>/dev/null || printf '?') entries)"
            else
                warn "ignoring $1: expected a JSON object such as {\"alias\":\"real-model\"}"
            fi
        fi
    }

    # Credentials.
    apply_list O2A_SERVER_KEYS '.server_keys' 'server_keys'
    apply_list O2A_ZEN_KEYS '.zen_keys' 'zen_keys'
    apply_list O2A_GO_KEYS '.go_keys' 'go_keys'
    o2a_drop_password_hash=0
    if o2a_raw=$(o2a_read O2A_WEBUI_PASSWORD); then
        patch_str '.webui.password' "$o2a_raw"
        # The stored hash must go, otherwise the Go runtime keeps verifying the
        # old password and ignores this plaintext bootstrap value. The deletion
        # has to happen on the merged document, since a JSON merge patch cannot
        # remove a key that only exists in the base config.
        o2a_drop_password_hash=1
        note "override webui.password (redacted; webui.password_hash cleared for re-hash)"
    fi

    # Upstream and proxies.
    apply_bool O2A_ANONYMOUS '.anonymous' 'anonymous'
    apply_str O2A_UPSTREAM_ZEN '.upstream.zen' 'upstream.zen'
    apply_str O2A_UPSTREAM_GO '.upstream.go' 'upstream.go'
    apply_list O2A_PROXIES '.proxies' 'proxies'
    apply_str O2A_PROXYFILE '.proxyfile' 'proxyfile'

    # Runtime behavior.
    apply_int O2A_RETRY_MAX_ATTEMPTS '.retry.max_attempts' 'retry.max_attempts'
    apply_int O2A_RETRY_TIMEOUT_SECONDS '.retry.timeout_seconds' 'retry.timeout_seconds'
    apply_int O2A_MODELS_REFRESH_SECONDS '.models.refresh_seconds' 'models.refresh_seconds'
    apply_json_object O2A_MODEL_ALIASES '.models.aliases' 'models.aliases'
    apply_str O2A_LOG_LEVEL '.logging.level' 'logging.level'
    apply_str O2A_PREFER '.prefer' 'prefer'
    apply_str O2A_REASONING_EFFORT '.reasoning.effort' 'reasoning.effort'

    # Listeners. An explicit -listen / -web-listen flag already wins at
    # runtime, so the flag keeps priority when both are provided.
    if [ -z "$listen_address" ]; then
        apply_str O2A_LISTEN '.listen' 'listen'
    fi
    if [ -z "$webui_listen_address" ]; then
        apply_str O2A_WEBUI_LISTEN '.webui.listen' 'webui.listen'
    fi

    # WebUI and rotation.
    apply_bool O2A_WEBUI_ENABLED '.webui.enabled' 'webui.enabled'
    apply_str O2A_WEBUI_USERNAME '.webui.username' 'webui.username'
    apply_list O2A_SOTA_ORDER '.rotation.sota.order' 'rotation.sota.order'
    apply_list O2A_SWEET_ORDER '.rotation.sweet.order' 'rotation.sweet.order'
    apply_int O2A_FIRST_OUTPUT_SECONDS '.rotation.first_output_seconds' 'rotation.first_output_seconds'

    if [ "$(jq 'length' "$o2a_patch")" -gt 0 ]; then
        o2a_filter='.[0] * .[1]'
        if [ "$o2a_drop_password_hash" = "1" ]; then
            o2a_filter='(.[0] * .[1]) | del(.webui.password_hash)'
        fi
        if jq -s "$o2a_filter" "$config_path" "$o2a_patch" >"$o2a_work/merged.json" 2>"$o2a_work/jq.err"; then
            chmod 600 "$o2a_work/merged.json" 2>/dev/null || true
            mv "$o2a_work/merged.json" "$config_path"
            note "applied environment overrides to $config_path"
        else
            warn "could not apply environment overrides; keeping $config_path unchanged: $(cat "$o2a_work/jq.err")"
        fi
    fi

    rm -rf "$o2a_work"
}

overlay_config

set -- /usr/local/bin/opencode2api -config "$config_path"
if [ -n "$listen_address" ]; then
    set -- "$@" -listen "$listen_address"
fi
if [ -n "$webui_listen_address" ]; then
    set -- "$@" -web-listen "$webui_listen_address"
fi
exec "$@"
