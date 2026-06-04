# shellcheck shell=bash
# Source this to get the `fleet` and `fleet-bridge` shell functions.
# Works under bash and zsh, interactive or not.

_ACP_SHEPHERD_DIR="${BASH_SOURCE[0]:-${(%):-%x}}"
_ACP_SHEPHERD_DIR="$(cd "$(dirname "$_ACP_SHEPHERD_DIR")/.." && pwd)"
export ACP_SHEPHERD_DIR="$_ACP_SHEPHERD_DIR"

fleet()        { python3 "$ACP_SHEPHERD_DIR/scripts/fleet.py"        "$@"; }
fleet-bridge() { python3 "$ACP_SHEPHERD_DIR/scripts/fleet_bridge.py" "$@"; }
fleet-loop()   { python3 "$ACP_SHEPHERD_DIR/scripts/fleet_loop.py"   "$@"; }
